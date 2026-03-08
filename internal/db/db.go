package db

import (
	"encoding/binary"
	"log"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Chocola-X/NekoIPinfo/internal/codec"
	"github.com/Chocola-X/NekoIPinfo/internal/model"
	"github.com/Chocola-X/NekoIPinfo/internal/sysinfo"
	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/bloom"
	lru "github.com/hashicorp/golang-lru/v2"
)

const (
	MemModeOff  = "off"
	MemModeFast = "fast"
	MemModeFull = "full"
)

type cacheEntry struct {
	data []byte
}

type Store struct {
	PDB      *pebble.DB
	Rules    []model.CompactRule
	DataPool []byte
	MemMode  string
	lruCache *lru.Cache[uint64, cacheEntry]
	iterPool sync.Pool

	activeReqs atomic.Int64
	stopCh     chan struct{}
}

func Open(dbPath string, memMode string, memBudget int64) (*Store, error) {
	dbSizeMB := sysinfo.DatabaseSizeMB(dbPath)
	availMB := sysinfo.AvailableMemoryMB()
	dbSizeBytes := int64(dbSizeMB * 1024 * 1024)

	if memBudget > 0 {
		budgetMB := memBudget / (1024 * 1024)
		if memMode == MemModeFull {
			needBytes := dbSizeBytes * 12
			if needBytes > memBudget {
				log.Printf("全量内存需要约 %d MB，内存预算 %d MB，降级为 fast 模式", needBytes/(1024*1024), budgetMB)
				memMode = MemModeFast
			}
		}
		if memMode == MemModeFast {
			needBytes := dbSizeBytes * 2
			if needBytes > memBudget {
				log.Printf("fast 模式需要约 %d MB，内存预算 %d MB，降级为 off 模式", needBytes/(1024*1024), budgetMB)
				memMode = MemModeOff
			}
		}
	} else {
		if memMode == MemModeFull {
			needMB := uint64(dbSizeMB * 12)
			if needMB > 0 && availMB > 0 && needMB > availMB {
				log.Printf("全量内存需要约 %d MB，可用内存 %d MB，降级为 fast 模式", needMB, availMB)
				memMode = MemModeFast
			}
		}
		if memMode == MemModeFast {
			needMB := uint64(dbSizeMB * 2)
			if needMB > 0 && availMB > 0 && needMB > availMB {
				log.Printf("fast 模式需要约 %d MB，可用内存 %d MB，降级为 off 模式", needMB, availMB)
				memMode = MemModeOff
			}
		}
	}

	var cacheSize int64

	if memBudget > 0 {
		cacheSize = memBudget * 40 / 100
		if cacheSize < 4<<20 {
			cacheSize = 4 << 20
		}
		if cacheSize > memBudget*60/100 {
			cacheSize = memBudget * 60 / 100
		}
		if cacheSize > 512<<20 {
			cacheSize = 512 << 20
		}
	} else {
		totalMem := int64(sysinfo.TotalMemoryMB())
		if totalMem <= 0 {
			totalMem = 1024
		}
		switch memMode {
		case MemModeFull:
			cacheSize = 64 << 20
		case MemModeFast:
			if totalMem > 2048 {
				cacheSize = 256 << 20
			} else if totalMem > 1024 {
				cacheSize = 128 << 20
			} else {
				cacheSize = 64 << 20
			}
		default:
			if totalMem > 2048 {
				cacheSize = 256 << 20
			} else if totalMem > 1024 {
				cacheSize = 128 << 20
			} else if totalMem > 512 {
				cacheSize = 64 << 20
			} else {
				cacheSize = 32 << 20
			}
		}
	}

	cache := pebble.NewCache(cacheSize)
	defer cache.Unref()

	filterPolicy := bloom.FilterPolicy(10)
	levelOpts := make([]pebble.LevelOptions, 7)
	for i := range levelOpts {
		levelOpts[i] = pebble.LevelOptions{FilterPolicy: filterPolicy}
	}

	opts := &pebble.Options{
		ReadOnly: true,
		Cache:    cache,
		Levels:   levelOpts,
	}

	pdb, err := pebble.Open(dbPath, opts)
	if err != nil {
		return nil, err
	}

	s := &Store{
		PDB:     pdb,
		MemMode: memMode,
		stopCh:  make(chan struct{}),
	}

	log.Printf("Pebble 缓存: %d MB | 数据库大小: %.1f MB | 系统可用内存: %d MB",
		cacheSize>>20, dbSizeMB, availMB)

	switch memMode {
	case MemModeFull:
		if err := s.loadFullToMemory(); err != nil {
			pdb.Close()
			return nil, err
		}
	case MemModeFast:
		if err := s.loadIndexToMemory(); err != nil {
			pdb.Close()
			return nil, err
		}
		lruSize := 100000
		if memBudget > 0 {
			lruSize = int(memBudget / (1024 * 4))
			if lruSize < 1000 {
				lruSize = 1000
			}
			if lruSize > 500000 {
				lruSize = 500000
			}
		} else {
			if availMB > 2048 {
				lruSize = 500000
			} else if availMB > 1024 {
				lruSize = 200000
			} else if availMB < 512 {
				lruSize = 50000
			}
		}
		s.lruCache, err = lru.New[uint64, cacheEntry](lruSize)
		if err != nil {
			pdb.Close()
			return nil, err
		}
		log.Printf("LRU 缓存容量: %d 条", lruSize)
	default:
		s.iterPool = sync.Pool{
			New: func() interface{} {
				iter, iterErr := pdb.NewIter(nil)
				if iterErr != nil {
					return nil
				}
				return iter
			},
		}
	}

	go s.idleCleanupLoop()

	return s, nil
}

func (s *Store) Close() {
	if s.stopCh != nil {
		close(s.stopCh)
	}
	if s.PDB != nil {
		s.PDB.Close()
	}
}

func (s *Store) IncrActive() {
	s.activeReqs.Add(1)
}

func (s *Store) DecrActive() {
	s.activeReqs.Add(-1)
}

func (s *Store) idleCleanupLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	var idleRounds int

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			active := s.activeReqs.Load()
			if active > 0 {
				idleRounds = 0
				continue
			}
			idleRounds++
			if idleRounds >= 3 {
				s.iterPool = sync.Pool{}
				runtime.GC()
				debug.FreeOSMemory()
				idleRounds = 0
			}
		}
	}
}

func decodeIP128(b []byte) (uint64, uint64) {
	return binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:16])
}

func ip128Greater(aHi, aLo, bHi, bLo uint64) bool {
	return aHi > bHi || (aHi == bHi && aLo > bLo)
}

func ip128LessOrEqual(aHi, aLo, bHi, bLo uint64) bool {
	return aHi < bHi || (aHi == bHi && aLo <= bLo)
}

func (s *Store) searchRules(ipHi, ipLo uint64) *model.CompactRule {
	rules := s.Rules
	idx := sort.Search(len(rules), func(i int) bool {
		return ip128Greater(rules[i].StartHi, rules[i].StartLo, ipHi, ipLo)
	})
	if idx > 0 {
		rule := &rules[idx-1]
		if ip128LessOrEqual(ipHi, ipLo, rule.EndHi, rule.EndLo) {
			return rule
		}
	}
	return nil
}

func decodePayload(raw []byte) []byte {
	r, err := codec.DecodeAuto(raw)
	if err != nil {
		return raw
	}
	return codec.ToJSON(r)
}

func (s *Store) loadFullToMemory() error {
	log.Println("正在将数据库全量载入内存，请稍候喵...")
	startTime := time.Now()

	iter, err := s.PDB.NewIter(nil)
	if err != nil {
		return err
	}
	defer iter.Close()

	rules := make([]model.CompactRule, 0, 1<<20)
	dataPool := make([]byte, 0, 128<<20)

	for valid := iter.First(); valid; valid = iter.Next() {
		keyBytes := iter.Key()
		valBytes, err := iter.ValueAndErr()
		if err != nil {
			continue
		}
		if len(keyBytes) != 16 || len(valBytes) < 16 {
			continue
		}

		startHi, startLo := decodeIP128(keyBytes)
		endHi, endLo := decodeIP128(valBytes[:16])
		payload := valBytes[16:]

		jsonData := decodePayload(payload)

		offset := uint32(len(dataPool))
		dataPool = append(dataPool, jsonData...)

		rules = append(rules, model.CompactRule{
			StartHi:    startHi,
			StartLo:    startLo,
			EndHi:      endHi,
			EndLo:      endLo,
			JsonOffset: offset,
			JsonLen:    uint32(len(jsonData)),
		})
	}

	s.Rules = rules
	s.DataPool = dataPool

	log.Printf("全量载入完成！共 %d 条规则，数据池 %.2f MB，耗时 %v 喵！",
		len(s.Rules), float64(len(s.DataPool))/(1024*1024), time.Since(startTime))

	return nil
}

func (s *Store) loadIndexToMemory() error {
	log.Println("正在将索引载入内存（fast 模式），请稍候喵...")
	startTime := time.Now()

	iter, err := s.PDB.NewIter(nil)
	if err != nil {
		return err
	}
	defer iter.Close()

	rules := make([]model.CompactRule, 0, 1<<20)

	for valid := iter.First(); valid; valid = iter.Next() {
		keyBytes := iter.Key()
		valBytes, err := iter.ValueAndErr()
		if err != nil {
			continue
		}
		if len(keyBytes) != 16 || len(valBytes) < 16 {
			continue
		}

		startHi, startLo := decodeIP128(keyBytes)
		endHi, endLo := decodeIP128(valBytes[:16])

		rules = append(rules, model.CompactRule{
			StartHi: startHi,
			StartLo: startLo,
			EndHi:   endHi,
			EndLo:   endLo,
		})
	}

	s.Rules = rules

	log.Printf("索引载入完成！共 %d 条规则，耗时 %v 喵！",
		len(s.Rules), time.Since(startTime))

	return nil
}

func (s *Store) LookupFull(ipHi, ipLo uint64) ([]byte, bool) {
	s.IncrActive()
	defer s.DecrActive()

	rule := s.searchRules(ipHi, ipLo)
	if rule != nil {
		return s.DataPool[rule.JsonOffset : rule.JsonOffset+rule.JsonLen], true
	}
	return nil, false
}

func makeCacheKey(hi, lo uint64) uint64 {
	return hi ^ (lo * 0x9e3779b97f4a7c15)
}

func (s *Store) LookupFast(ipHi, ipLo uint64) ([]byte, bool) {
	s.IncrActive()
	defer s.DecrActive()

	rule := s.searchRules(ipHi, ipLo)
	if rule == nil {
		return nil, false
	}

	ck := makeCacheKey(rule.StartHi, rule.StartLo)
	if s.lruCache != nil {
		if entry, ok := s.lruCache.Get(ck); ok {
			return entry.data, true
		}
	}

	var key [16]byte
	binary.BigEndian.PutUint64(key[:8], rule.StartHi)
	binary.BigEndian.PutUint64(key[8:], rule.StartLo)

	val, closer, err := s.PDB.Get(key[:])
	if err != nil {
		return nil, false
	}
	defer closer.Close()

	if len(val) < 16 {
		return nil, false
	}

	result := decodePayload(val[16:])
	resultCopy := make([]byte, len(result))
	copy(resultCopy, result)

	if s.lruCache != nil {
		s.lruCache.Add(ck, cacheEntry{data: resultCopy})
	}

	return resultCopy, true
}

func (s *Store) LookupPebble(ipHi, ipLo uint64) ([]byte, error) {
	s.IncrActive()
	defer s.DecrActive()

	var seekKey [17]byte
	binary.BigEndian.PutUint64(seekKey[:8], ipHi)
	binary.BigEndian.PutUint64(seekKey[8:16], ipLo)
	seekKey[16] = 0

	poolObj := s.iterPool.Get()
	if poolObj == nil {
		iter, err := s.PDB.NewIter(nil)
		if err != nil {
			return nil, err
		}
		poolObj = iter
	}
	iter := poolObj.(*pebble.Iterator)

	returnIter := func() {
		s.iterPool.Put(iter)
	}

	if !iter.SeekLT(seekKey[:]) {
		if !iter.First() {
			returnIter()
			return nil, nil
		}
		keyBytes := iter.Key()
		if len(keyBytes) != 16 {
			returnIter()
			return nil, nil
		}
		kHi, kLo := decodeIP128(keyBytes)
		if ip128Greater(kHi, kLo, ipHi, ipLo) {
			returnIter()
			return nil, nil
		}
	}

	keyBytes := iter.Key()
	if len(keyBytes) != 16 {
		returnIter()
		return nil, nil
	}

	kHi, kLo := decodeIP128(keyBytes)
	if ip128Greater(kHi, kLo, ipHi, ipLo) {
		returnIter()
		return nil, nil
	}

	valBytes, err := iter.ValueAndErr()
	if err != nil {
		returnIter()
		return nil, err
	}

	if len(valBytes) < 16 {
		returnIter()
		return nil, nil
	}

	endHi, endLo := decodeIP128(valBytes[:16])

	if ip128Greater(ipHi, ipLo, endHi, endLo) {
		returnIter()
		return nil, nil
	}

	result := decodePayload(valBytes[16:])
	resultCopy := make([]byte, len(result))
	copy(resultCopy, result)

	returnIter()
	return resultCopy, nil
}