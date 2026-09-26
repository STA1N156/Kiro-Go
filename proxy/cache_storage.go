package proxy

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"kiro-go/config"
	"kiro-go/logger"
	"os"
	"path/filepath"
	"time"
)

const cacheFileMagic = "KPCACHE1"

// The compact index is at most ~10 MiB (131072 fixed-size records), well below
// the 512 MiB policy ceiling. It contains neither prompts nor credentials.
func (t *promptCacheTracker) loadIndex(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	header := make([]byte, len(cacheFileMagic))
	if _, err := io.ReadFull(r, header); err != nil {
		return err
	}
	if string(header) != cacheFileMagic {
		return fmt.Errorf("unknown prompt cache format")
	}
	cutoff := time.Now().Add(-time.Duration(config.GetLocalCacheSettings().TTLMinutes) * time.Minute).UnixNano()
	for count := 0; count < cacheShardCount*cacheEntriesPerShard; count++ {
		var entry promptCacheEntry
		if err := binary.Read(r, binary.LittleEndian, &entry); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if entry.UpdatedAt <= cutoff || entry.UpdatedAt > time.Now().UnixNano() || entry.Tokens < 0 {
			continue
		}
		shard := &t.shards[int(entry.Hash[0])%cacheShardCount]
		if len(shard.entries) < cacheEntriesPerShard {
			shard.entries[entry.Hash] = entry
		}
	}
	return nil
}

func (t *promptCacheTracker) saveIndex(path string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".prompt-cache-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	w := bufio.NewWriterSize(f, 64*1024)
	if _, err := w.WriteString(cacheFileMagic); err != nil {
		return err
	}
	cutoff := time.Now().Add(-time.Duration(config.GetLocalCacheSettings().TTLMinutes) * time.Minute).UnixNano()
	// Copy one small shard at a time, then encode/write without holding its lock.
	entries := make([]promptCacheEntry, 0, cacheEntriesPerShard)
	for i := range t.shards {
		shard := &t.shards[i]
		entries = entries[:0]
		shard.Lock()
		for key, entry := range shard.entries {
			if entry.UpdatedAt <= cutoff {
				delete(shard.entries, key)
			} else {
				entries = append(entries, entry)
			}
		}
		shard.Unlock()
		for _, entry := range entries {
			if err := binary.Write(w, binary.LittleEndian, entry); err != nil {
				return err
			}
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (t *promptCacheTracker) persistIndex(path string, stop <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := t.saveIndex(path); err != nil {
				logger.Warnf("[PromptCache] save failed: %v", err)
			}
		case <-stop:
			return
		}
	}
}
