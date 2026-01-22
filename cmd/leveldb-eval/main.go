package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

const (
	defaultBlockSize       = 32 * 1024
	defaultRestartInterval = 16
	defaultWriteBuffer     = 64 * 1024 * 1024
	defaultBatchBytes      = 32 * 1024 * 1024
)

const (
	transformBefore = "before"
	transformAfter1 = "after1"
	transformAfter2 = "after2"
)

const (
	compressionSnappy = "snappy"
	compressionNone   = "none"
)

const (
	baseAccountTypeURL     = "/cosmos.auth.v1beta1.BaseAccount"
	moduleAccountTypeURL   = "/cosmos.auth.v1beta1.ModuleAccount"
	delayedVestingTypeURL  = "/cosmos.vesting.v1beta1.DelayedVestingAccount"
	continuousVestingURL   = "/cosmos.vesting.v1beta1.ContinuousVestingAccount"
	periodicVestingTypeURL = "/cosmos.vesting.v1beta1.PeriodicVestingAccount"
	permanentLockedURL     = "/cosmos.vesting.v1beta1.PermanentLockedAccount"
)

var typeURLToID = map[string]uint64{
	baseAccountTypeURL:     1,
	delayedVestingTypeURL:  2,
	continuousVestingURL:   3,
	moduleAccountTypeURL:   4,
	periodicVestingTypeURL: 5,
	permanentLockedURL:     6,
}

func main() {
	var (
		input           = flag.String("input", "", "input JSONL file")
		outDir          = flag.String("out", "", "output directory")
		limit           = flag.Int("limit", 0, "max records to ingest (0 = all)")
		compression     = flag.String("compression", compressionSnappy, "snappy|none")
		blockSize       = flag.Int("block-size", defaultBlockSize, "LevelDB data block size")
		restartInterval = flag.Int("restart-interval", defaultRestartInterval, "LevelDB block restart interval")
		writeBuffer     = flag.Int("write-buffer", defaultWriteBuffer, "LevelDB write buffer size")
		batchBytes      = flag.Int("batch-bytes", defaultBatchBytes, "max bytes per write batch")
		transform       = flag.String("transform", transformBefore, "before|after1|after2")
		runBench        = flag.Bool("run-bench", false, "run read benchmarks")
		benchGets       = flag.Int("bench-gets", 1_000_000, "number of random gets")
		benchScan       = flag.Bool("bench-scan", false, "run full scan benchmark")
		name            = flag.String("name", "", "optional variant name")
	)
	flag.Parse()

	if *input == "" || *outDir == "" {
		fatalf("--input and --out are required")
	}

	if *compression != compressionSnappy && *compression != compressionNone {
		fatalf("invalid --compression: %s", *compression)
	}
	if *transform != transformBefore && *transform != transformAfter1 && *transform != transformAfter2 {
		fatalf("invalid --transform: %s", *transform)
	}
	if *blockSize <= 0 || *restartInterval <= 0 || *writeBuffer <= 0 || *batchBytes <= 0 {
		fatalf("block size, restart interval, write buffer, and batch bytes must be > 0")
	}

	variantName := *name
	if variantName == "" {
		variantName = fmt.Sprintf("%s-%s", *transform, *compression)
	}

	variantDir := filepath.Join(*outDir, variantName)
	dbDir := filepath.Join(variantDir, "db")

	if err := os.RemoveAll(variantDir); err != nil {
		fatalf("remove output dir: %v", err)
	}
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		fatalf("create output dir: %v", err)
	}

	fmt.Printf("variant=%s transform=%s compression=%s\n", variantName, *transform, *compression)

	opts := &opt.Options{
		BlockSize:            *blockSize,
		BlockRestartInterval: *restartInterval,
		WriteBuffer:          *writeBuffer,
	}
	if *compression == compressionNone {
		opts.Compression = opt.NoCompression
	} else {
		opts.Compression = opt.SnappyCompression
	}

	db, err := leveldb.OpenFile(dbDir, opts)
	if err != nil {
		fatalf("open db: %v", err)
	}

	sampler := (*keySampler)(nil)
	if *runBench && *benchGets > 0 {
		sampler = newKeySampler(*benchGets)
	}

	ingestStart := time.Now()
	stats, err := ingest(db, *input, *limit, *batchBytes, *transform, sampler)
	if err != nil {
		fatalf("ingest: %v", err)
	}
	ingestSeconds := time.Since(ingestStart).Seconds()

	compactionStart := time.Now()
	if err := db.CompactRange(util.Range{}); err != nil {
		fatalf("compact: %v", err)
	}

	props := collectProperties(db)

	if err := db.Close(); err != nil {
		fatalf("close: %v", err)
	}
	compactionSeconds := time.Since(compactionStart).Seconds()

	if err := writeProperties(variantDir, props); err != nil {
		fatalf("write properties: %v", err)
	}

	sstBytes, dirBytes, sstFiles, err := measureDir(dbDir)
	if err != nil {
		fatalf("measure dir: %v", err)
	}

	var bench BenchResult
	if *runBench && *benchGets > 0 {
		bench, err = runBenchmarks(dbDir, opts, sampler, *benchGets, *benchScan)
		if err != nil {
			fatalf("bench: %v", err)
		}
	}

	res := Result{
		Timestamp:        time.Now().Format(time.RFC3339),
		Variant:          variantName,
		InputPath:        *input,
		DBPath:           dbDir,
		Transform:        *transform,
		Compression:      *compression,
		BlockSize:        *blockSize,
		RestartInterval:  *restartInterval,
		WriteBuffer:      *writeBuffer,
		BatchBytes:       *batchBytes,
		Limit:            *limit,
		Records:          stats.records,
		BytesIn:          stats.bytesIn,
		BytesOut:         stats.bytesOut,
		IngestSeconds:    ingestSeconds,
		CompactSeconds:   compactionSeconds,
		SSTBytes:         sstBytes,
		DirBytes:         dirBytes,
		SSTFiles:         sstFiles,
		BenchGets:        bench.Gets,
		BenchGetsPerSec:  bench.GetsPerSec,
		BenchScan:        bench.Scan,
		BenchScanBytes:   bench.ScanBytes,
		BenchScanMBPerS:  bench.ScanMBPerSec,
		BenchScanSeconds: bench.ScanSeconds,
	}

	if err := writeResults(variantDir, res); err != nil {
		fatalf("write results: %v", err)
	}

	fmt.Printf("records=%d sst_bytes=%d dir_bytes=%d\n", res.Records, res.SSTBytes, res.DirBytes)
	if res.BenchGets > 0 {
		fmt.Printf("gets_ops_s=%.2f\n", res.BenchGetsPerSec)
	}
	if res.BenchScan {
		fmt.Printf("scan_mb_s=%.2f\n", res.BenchScanMBPerS)
	}
}

func ingest(db *leveldb.DB, input string, limit int, batchBytes int, transform string, sampler *keySampler) (ingestStats, error) {
	file, err := os.Open(input)
	if err != nil {
		return ingestStats{}, err
	}
	defer file.Close()

	reader := bufio.NewScanner(file)
	buf := make([]byte, 0, 1024*1024)
	reader.Buffer(buf, 32*1024*1024)

	batch := new(leveldb.Batch)
	batchSize := 0
	var stats ingestStats
	var prevKey []byte

	flush := func() error {
		if batch.Len() == 0 {
			return nil
		}
		if err := db.Write(batch, &opt.WriteOptions{Sync: false}); err != nil {
			return err
		}
		batch.Reset()
		batchSize = 0
		return nil
	}

	for reader.Scan() {
		line := reader.Bytes()
		key, val, err := decodeLine(line)
		if err != nil {
			return stats, err
		}
		if prevKey != nil && bytes.Compare(prevKey, key) > 0 {
			return stats, fmt.Errorf("input keys not sorted: %q < %q", key, prevKey)
		}
		prevKey = append(prevKey[:0], key...)

		outVal := val
		switch transform {
		case transformBefore:
			// noop
		case transformAfter1:
			outVal = transformAfter1Fn(val)
		case transformAfter2:
			outVal = transformAfter2Fn(val)
		default:
			return stats, fmt.Errorf("unknown transform: %s", transform)
		}

		batch.Put(key, outVal)
		batchSize += len(key) + len(outVal)
		stats.records++
		stats.bytesIn += int64(len(val))
		stats.bytesOut += int64(len(outVal))

		if sampler != nil {
			sampler.Add(key)
		}

		if batchSize >= batchBytes {
			if err := flush(); err != nil {
				return stats, err
			}
		}

		if limit > 0 && stats.records >= limit {
			break
		}
	}

	if err := reader.Err(); err != nil {
		return stats, err
	}

	if err := flush(); err != nil {
		return stats, err
	}

	return stats, nil
}

type ingestStats struct {
	records  int
	bytesIn  int64
	bytesOut int64
}

type BenchResult struct {
	Gets         int
	GetsPerSec   float64
	Scan         bool
	ScanBytes    int64
	ScanMBPerSec float64
	ScanSeconds  float64
}

type Result struct {
	Timestamp        string  `json:"timestamp"`
	Variant          string  `json:"variant"`
	InputPath        string  `json:"input_path"`
	DBPath           string  `json:"db_path"`
	Transform        string  `json:"transform"`
	Compression      string  `json:"compression"`
	BlockSize        int     `json:"block_size"`
	RestartInterval  int     `json:"restart_interval"`
	WriteBuffer      int     `json:"write_buffer"`
	BatchBytes       int     `json:"batch_bytes"`
	Limit            int     `json:"limit"`
	Records          int     `json:"records"`
	BytesIn          int64   `json:"bytes_in"`
	BytesOut         int64   `json:"bytes_out"`
	IngestSeconds    float64 `json:"ingest_seconds"`
	CompactSeconds   float64 `json:"compact_seconds"`
	SSTBytes         int64   `json:"sst_bytes"`
	DirBytes         int64   `json:"dir_bytes"`
	SSTFiles         int     `json:"sst_files"`
	BenchGets        int     `json:"bench_gets"`
	BenchGetsPerSec  float64 `json:"bench_gets_per_sec"`
	BenchScan        bool    `json:"bench_scan"`
	BenchScanBytes   int64   `json:"bench_scan_bytes"`
	BenchScanMBPerS  float64 `json:"bench_scan_mb_per_sec"`
	BenchScanSeconds float64 `json:"bench_scan_seconds"`
}

type jsonLine struct {
	Key      string `json:"key"`
	Val      string `json:"val"`
	Value    string `json:"value"`
	Encoding string `json:"encoding"`
}

func decodeLine(line []byte) ([]byte, []byte, error) {
	var rec jsonLine
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, nil, err
	}
	if rec.Encoding != "" && rec.Encoding != "base64" {
		return nil, nil, fmt.Errorf("unexpected encoding: %s", rec.Encoding)
	}
	valStr := rec.Val
	if valStr == "" {
		valStr = rec.Value
	}
	key, err := base64.StdEncoding.DecodeString(rec.Key)
	if err != nil {
		return nil, nil, err
	}
	val, err := base64.StdEncoding.DecodeString(valStr)
	if err != nil {
		return nil, nil, err
	}
	return key, val, nil
}

func measureDir(dir string) (sstBytes int64, dirBytes int64, sstFiles int, err error) {
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		size := info.Size()
		dirBytes += size
		if strings.HasSuffix(path, ".sst") || strings.HasSuffix(path, ".ldb") {
			sstBytes += size
			sstFiles++
		}
		return nil
	})
	return sstBytes, dirBytes, sstFiles, err
}

func collectProperties(db *leveldb.DB) map[string]string {
	props := []string{
		"leveldb.stats",
		"leveldb.sstables",
		"leveldb.blockpool",
	}
	out := make(map[string]string)
	for _, name := range props {
		if val, err := db.GetProperty(name); err == nil && val != "" {
			out[name] = val
		}
	}
	return out
}

func writeProperties(dir string, props map[string]string) error {
	if len(props) == 0 {
		return nil
	}
	path := filepath.Join(dir, "properties.txt")
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	for name, val := range props {
		if _, err := fmt.Fprintf(file, "[%s]\n%s\n\n", name, val); err != nil {
			return err
		}
	}
	return nil
}

func runBenchmarks(dbDir string, opts *opt.Options, sampler *keySampler, benchGets int, benchScan bool) (BenchResult, error) {
	benchOpts := *opts
	benchOpts.ReadOnly = true
	db, err := leveldb.OpenFile(dbDir, &benchOpts)
	if err != nil {
		return BenchResult{}, err
	}
	defer db.Close()

	res := BenchResult{}
	if sampler != nil && benchGets > 0 {
		keys := sampler.Keys()
		if len(keys) == 0 {
			return res, errors.New("no keys sampled for bench")
		}
		ops := benchGets
		if ops > len(keys) {
			ops = len(keys)
		}
		start := time.Now()
		for i := 0; i < ops; i++ {
			if _, err := db.Get(keys[i], nil); err != nil && !errors.Is(err, leveldb.ErrNotFound) {
				return res, err
			}
		}
		elapsed := time.Since(start).Seconds()
		res.Gets = ops
		if elapsed > 0 {
			res.GetsPerSec = float64(ops) / elapsed
		}
	}

	if benchScan {
		iter := db.NewIterator(nil, nil)
		start := time.Now()
		var total int64
		for iter.Next() {
			total += int64(len(iter.Key()) + len(iter.Value()))
		}
		elapsed := time.Since(start).Seconds()
		if err := iter.Error(); err != nil {
			iter.Release()
			return res, err
		}
		iter.Release()
		res.Scan = true
		res.ScanBytes = total
		res.ScanSeconds = elapsed
		if elapsed > 0 {
			res.ScanMBPerSec = float64(total) / (1024.0 * 1024.0) / elapsed
		}
	}

	return res, nil
}

func writeResults(dir string, res Result) error {
	jsonPath := filepath.Join(dir, "results.json")
	jsonBytes, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(jsonPath, jsonBytes, 0o644); err != nil {
		return err
	}

	mdPath := filepath.Join(dir, "results.md")
	md := &bytes.Buffer{}
	fmt.Fprintf(md, "| metric | value |\n|---|---|\n")
	fmt.Fprintf(md, "| variant | %s |\n", res.Variant)
	fmt.Fprintf(md, "| transform | %s |\n", res.Transform)
	fmt.Fprintf(md, "| compression | %s |\n", res.Compression)
	fmt.Fprintf(md, "| block_size | %d |\n", res.BlockSize)
	fmt.Fprintf(md, "| restart_interval | %d |\n", res.RestartInterval)
	fmt.Fprintf(md, "| write_buffer | %d |\n", res.WriteBuffer)
	fmt.Fprintf(md, "| batch_bytes | %d |\n", res.BatchBytes)
	fmt.Fprintf(md, "| records | %d |\n", res.Records)
	fmt.Fprintf(md, "| bytes_in | %d |\n", res.BytesIn)
	fmt.Fprintf(md, "| bytes_out | %d |\n", res.BytesOut)
	fmt.Fprintf(md, "| ingest_s | %.3f |\n", res.IngestSeconds)
	fmt.Fprintf(md, "| compact_s | %.3f |\n", res.CompactSeconds)
	fmt.Fprintf(md, "| sst_bytes | %d |\n", res.SSTBytes)
	fmt.Fprintf(md, "| dir_bytes | %d |\n", res.DirBytes)
	fmt.Fprintf(md, "| sst_files | %d |\n", res.SSTFiles)
	if res.BenchGets > 0 {
		fmt.Fprintf(md, "| gets_ops_s | %.2f |\n", res.BenchGetsPerSec)
	}
	if res.BenchScan {
		fmt.Fprintf(md, "| scan_mb_s | %.2f |\n", res.BenchScanMBPerS)
	}
	return os.WriteFile(mdPath, md.Bytes(), 0o644)
}

func fatalf(msg string, args ...any) {
	fmt.Fprintf(os.Stderr, msg+"\n", args...)
	os.Exit(1)
}

func transformAfter1Fn(val []byte) []byte {
	heightBytes, payload, ok := parseWrapper(val)
	if !ok {
		return val
	}
	typeURL, inner, ok := parseAny(payload)
	if !ok {
		return val
	}
	return buildAfter1(heightBytes, inner, typeURL)
}

func transformAfter2Fn(val []byte) []byte {
	heightBytes, payload, ok := parseWrapper(val)
	if !ok {
		return val
	}
	typeURL, inner, ok := parseAny(payload)
	if !ok {
		return val
	}
	if typeURL != baseAccountTypeURL {
		return buildAfter1(heightBytes, inner, typeURL)
	}
	acct, ok := parseBaseAccount(inner)
	if !ok {
		return buildAfter1(heightBytes, inner, typeURL)
	}
	addr, ok := decodeBech32Address(acct.address)
	if !ok {
		return buildAfter1(heightBytes, inner, typeURL)
	}

	payloadOut := buildAccountV1(addr, acct.pubKey, acct.hasPubKey, acct.accountNumber, acct.sequence, acct.hasSequence)
	return buildWrapped(heightBytes, payloadOut)
}

func buildAfter1(heightBytes, inner []byte, typeURL string) []byte {
	typeID := typeURLToID[typeURL]
	if typeID == 0 {
		typeID = 255
	}
	payload := make([]byte, 0, len(inner)+binary.MaxVarintLen64)
	payload = appendUvarint(payload, typeID)
	payload = append(payload, inner...)
	return buildWrapped(heightBytes, payload)
}

func buildAccountV1(addr20 []byte, pubKey []byte, hasPubKey bool, accountNumber uint64, sequence uint64, hasSequence bool) []byte {
	flags := byte(0)
	if hasPubKey {
		flags |= 0x01
	}
	if hasSequence {
		flags |= 0x02
	}

	payload := make([]byte, 0, 2+len(addr20)+len(pubKey)+binary.MaxVarintLen64*2)
	payload = append(payload, 1) // kind
	payload = append(payload, flags)
	payload = append(payload, addr20...)
	if hasPubKey {
		payload = append(payload, pubKey...)
	}
	payload = appendUvarint(payload, accountNumber)
	if hasSequence {
		payload = appendUvarint(payload, sequence)
	}
	return payload
}

type baseAccount struct {
	address       string
	pubKey        []byte
	hasPubKey     bool
	accountNumber uint64
	sequence      uint64
	hasSequence   bool
}

func parseBaseAccount(b []byte) (baseAccount, bool) {
	var out baseAccount
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return baseAccount{}, false
		}
		b = b[n:]
		field := key >> 3
		wire := key & 0x7
		switch wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return baseAccount{}, false
			}
			b = b[n:]
			switch field {
			case 3:
				out.accountNumber = v
			case 4:
				out.sequence = v
				out.hasSequence = true
			}
		case 1:
			if len(b) < 8 {
				return baseAccount{}, false
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || int(l) > len(b[n:]) {
				return baseAccount{}, false
			}
			data := b[n : n+int(l)]
			b = b[n+int(l):]
			switch field {
			case 1:
				out.address = string(data)
			case 2:
				if pubKey, ok := parsePubKeyAny(data); ok {
					out.pubKey = pubKey
					out.hasPubKey = true
				}
			}
		case 5:
			if len(b) < 4 {
				return baseAccount{}, false
			}
			b = b[4:]
		default:
			return baseAccount{}, false
		}
	}

	if out.address == "" {
		return baseAccount{}, false
	}
	return out, true
}

func parsePubKeyAny(b []byte) ([]byte, bool) {
	typeURL, inner, ok := parseAny(b)
	if !ok || !strings.Contains(typeURL, "secp256k1.PubKey") {
		return nil, false
	}
	pk, ok := parsePubKey(inner)
	if !ok {
		return nil, false
	}
	return pk, true
}

func parsePubKey(b []byte) ([]byte, bool) {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, false
		}
		b = b[n:]
		field := key >> 3
		wire := key & 0x7
		switch wire {
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || int(l) > len(b[n:]) {
				return nil, false
			}
			data := b[n : n+int(l)]
			b = b[n+int(l):]
			if field == 1 {
				if len(data) == 33 {
					out := make([]byte, len(data))
					copy(out, data)
					return out, true
				}
				return nil, false
			}
		case 0:
			_, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, false
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return nil, false
			}
			b = b[8:]
		case 5:
			if len(b) < 4 {
				return nil, false
			}
			b = b[4:]
		default:
			return nil, false
		}
	}
	return nil, false
}

func parseWrapper(val []byte) ([]byte, []byte, bool) {
	_, n := binary.Uvarint(val)
	if n <= 0 {
		return nil, nil, false
	}
	plen, n2 := binary.Uvarint(val[n:])
	if n2 <= 0 {
		return nil, nil, false
	}
	off := n + n2
	if off+int(plen) > len(val) {
		return nil, nil, false
	}
	heightBytes := append([]byte(nil), val[:n]...)
	payload := val[off : off+int(plen)]
	return heightBytes, payload, true
}

func parseAny(b []byte) (string, []byte, bool) {
	var typeURL string
	var inner []byte
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return "", nil, false
		}
		b = b[n:]
		field := key >> 3
		wire := key & 0x7
		switch wire {
		case 0:
			_, n := binary.Uvarint(b)
			if n <= 0 {
				return "", nil, false
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return "", nil, false
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || int(l) > len(b[n:]) {
				return "", nil, false
			}
			data := b[n : n+int(l)]
			b = b[n+int(l):]
			if field == 1 {
				typeURL = string(data)
			} else if field == 2 {
				inner = make([]byte, len(data))
				copy(inner, data)
			}
		case 5:
			if len(b) < 4 {
				return "", nil, false
			}
			b = b[4:]
		default:
			return "", nil, false
		}
	}
	if typeURL == "" || inner == nil {
		return "", nil, false
	}
	return typeURL, inner, true
}

func appendUvarint(dst []byte, v uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], v)
	return append(dst, buf[:n]...)
}

func buildWrapped(heightBytes, payload []byte) []byte {
	var lenBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lenBuf[:], uint64(len(payload)))
	out := make([]byte, 0, len(heightBytes)+n+len(payload))
	out = append(out, heightBytes...)
	out = append(out, lenBuf[:n]...)
	out = append(out, payload...)
	return out
}

type keySampler struct {
	limit int
	keys  [][]byte
	count uint64
	rng   uint64
}

func newKeySampler(limit int) *keySampler {
	return &keySampler{limit: limit, rng: 1}
}

func (s *keySampler) Add(key []byte) {
	if s.limit <= 0 {
		return
	}
	s.count++
	if len(s.keys) < s.limit {
		copyKey := make([]byte, len(key))
		copy(copyKey, key)
		s.keys = append(s.keys, copyKey)
		return
	}
	idx := s.next() % s.count
	if int(idx) < s.limit {
		copyKey := make([]byte, len(key))
		copy(copyKey, key)
		s.keys[idx] = copyKey
	}
}

func (s *keySampler) next() uint64 {
	x := s.rng
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	s.rng = x
	return x
}

func (s *keySampler) Keys() [][]byte {
	return s.keys
}
