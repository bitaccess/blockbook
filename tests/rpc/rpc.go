package rpc

import (
	"blockbook/bchain"
	"encoding/json"
	"errors"
	"io/ioutil"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deckarep/golang-set"
)

var testMap = map[string]func(t *testing.T, th *TestHandler){
	"GetBlockHash":             testGetBlockHash,
	"GetBlock":                 testGetBlock,
	"GetTransaction":           testGetTransaction,
	"GetTransactionForMempool": testGetTransactionForMempool,
	"MempoolSync":              testMempoolSync,
	"EstimateSmartFee":         testEstimateSmartFee,
	"EstimateFee":              testEstimateFee,
	"GetBestBlockHash":         testGetBestBlockHash,
	"GetBestBlockHeight":       testGetBestBlockHeight,
	"GetBlockHeader":           testGetBlockHeader,
}

type TestHandler struct {
	Chain    bchain.BlockChain
	TestData *TestData
}

type TestData struct {
	BlockHeight uint32                `json:"blockHeight"`
	BlockHash   string                `json:"blockHash"`
	BlockTime   int64                 `json:"blockTime"`
	BlockTxs    []string              `json:"blockTxs"`
	TxDetails   map[string]*bchain.Tx `json:"txDetails"`
}

func IntegrationTest(t *testing.T, coin string, chain bchain.BlockChain, testConfig json.RawMessage) {
	tests, err := getTests(testConfig)
	if err != nil {
		t.Fatalf("Failed loading of test list: %s", err)
	}

	parser := chain.GetChainParser()
	td, err := loadTestData(coin, parser)
	if err != nil {
		t.Fatalf("Failed loading of test data: %s", err)
	}

	h := TestHandler{Chain: chain, TestData: td}

	for _, test := range tests {
		if f, found := testMap[test]; found {
			t.Run(test, func(t *testing.T) { f(t, &h) })
		} else {
			t.Errorf("%s: test not found", test)
			continue
		}
	}
}

func getTests(cfg json.RawMessage) ([]string, error) {
	var v []string
	err := json.Unmarshal(cfg, &v)
	if err != nil {
		return nil, err
	}
	if len(v) == 0 {
		return nil, errors.New("No tests declared")
	}
	return v, nil
}

func loadTestData(coin string, parser bchain.BlockChainParser) (*TestData, error) {
	path := filepath.Join("rpc/testdata", coin+".json")
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v TestData
	err = json.Unmarshal(b, &v)
	if err != nil {
		return nil, err
	}
	for _, tx := range v.TxDetails {
		// convert amounts in test json to bit.Int and clear the temporary JsonValue
		for i := range tx.Vout {
			vout := &tx.Vout[i]
			vout.ValueSat, err = parser.AmountToBigInt(vout.JsonValue)
			if err != nil {
				return nil, err
			}
			vout.JsonValue = ""
		}

		// get addresses parsed
		err := setTxAddresses(parser, tx)
		if err != nil {
			return nil, err
		}
	}

	return &v, nil
}

func setTxAddresses(parser bchain.BlockChainParser, tx *bchain.Tx) error {
	// pack and unpack transaction in order to get addresses decoded - ugly but works
	var tmp *bchain.Tx
	b, err := parser.PackTx(tx, 0, 0)
	if err == nil {
		tmp, _, err = parser.UnpackTx(b)
		if err == nil {
			for i := 0; i < len(tx.Vout); i++ {
				tx.Vout[i].ScriptPubKey.Addresses = tmp.Vout[i].ScriptPubKey.Addresses
			}
		}
	}
	return err
}

func testGetBlockHash(t *testing.T, h *TestHandler) {
	hash, err := h.Chain.GetBlockHash(h.TestData.BlockHeight)
	if err != nil {
		t.Error(err)
		return
	}

	if hash != h.TestData.BlockHash {
		t.Errorf("GetBlockHash() got %q, want %q", hash, h.TestData.BlockHash)
	}
}
func testGetBlock(t *testing.T, h *TestHandler) {
	blk, err := h.Chain.GetBlock(h.TestData.BlockHash, 0)
	if err != nil {
		t.Error(err)
		return
	}

	if len(blk.Txs) != len(h.TestData.BlockTxs) {
		t.Errorf("GetBlock() number of transactions: got %d, want %d", len(blk.Txs), len(h.TestData.BlockTxs))
	}

	for ti, tx := range blk.Txs {
		if tx.Txid != h.TestData.BlockTxs[ti] {
			t.Errorf("GetBlock() transaction %d: got %s, want %s", ti, tx.Txid, h.TestData.BlockTxs[ti])
		}
	}
}
func testGetTransaction(t *testing.T, h *TestHandler) {
	for txid, want := range h.TestData.TxDetails {
		got, err := h.Chain.GetTransaction(txid)
		if err != nil {
			t.Error(err)
			return
		}
		// Confirmations is variable field, we just check if is set and reset it
		if got.Confirmations <= 0 {
			t.Errorf("GetTransaction() got struct with invalid Confirmations field")
			continue
		}
		got.Confirmations = 0

		if !reflect.DeepEqual(got, want) {
			t.Errorf("GetTransaction() got %+v, want %+v", got, want)
		}
	}
}
func testGetTransactionForMempool(t *testing.T, h *TestHandler) {
	for txid, want := range h.TestData.TxDetails {
		// reset fields that are not parsed by BlockChainParser
		want.Confirmations, want.Blocktime, want.Time = 0, 0, 0

		got, err := h.Chain.GetTransactionForMempool(txid)
		if err != nil {
			t.Fatal(err)
		}
		// transactions parsed from JSON may contain additional data
		got.Confirmations, got.Blocktime, got.Time = 0, 0, 0
		if !reflect.DeepEqual(got, want) {
			t.Errorf("GetTransactionForMempool() got %+v, want %+v", got, want)
		}
	}
}
func testMempoolSync(t *testing.T, h *TestHandler) {
	for i := 0; i < 3; i++ {
		txs := getMempool(t, h)

		n, err := h.Chain.ResyncMempool(nil)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			// no transactions to test
			continue
		}

		txs = intersect(txs, getMempool(t, h))
		if len(txs) == 0 {
			// no transactions to test
			continue
		}

		txid2addrs := getMempoolAddresses(t, h, txs)
		if len(txid2addrs) == 0 {
			t.Skip("Skipping test, no addresses in mempool")
		}

		for txid, addrs := range txid2addrs {
			for _, a := range addrs {
				got, err := h.Chain.GetMempoolTransactions(a)
				if err != nil {
					t.Fatal(err)
				}
				if !containsString(got, txid) {
					t.Errorf("ResyncMempool() - for address %s, transaction %s wasn't found in mempool", a, txid)
					return
				}
			}
		}

		// done
		return
	}
	t.Skip("Skipping test, all attempts to sync mempool failed due to network state changes")
}
func testEstimateSmartFee(t *testing.T, h *TestHandler) {
	for _, blocks := range []int{1, 2, 3, 5, 10} {
		fee, err := h.Chain.EstimateSmartFee(blocks, true)
		if err != nil {
			t.Error(err)
		}
		if fee.Sign() == -1 {
			sf := h.Chain.GetChainParser().AmountToDecimalString(&fee)
			if sf != "-1" {
				t.Errorf("EstimateSmartFee() returned unexpected fee rate: %v", sf)
			}
		}
	}
}
func testEstimateFee(t *testing.T, h *TestHandler) {
	for _, blocks := range []int{1, 2, 3, 5, 10} {
		fee, err := h.Chain.EstimateFee(blocks)
		if err != nil {
			t.Error(err)
		}
		if fee.Sign() == -1 {
			sf := h.Chain.GetChainParser().AmountToDecimalString(&fee)
			if sf != "-1" {
				t.Errorf("EstimateFee() returned unexpected fee rate: %v", sf)
			}
		}
	}
}
func testGetBestBlockHash(t *testing.T, h *TestHandler) {
	for i := 0; i < 3; i++ {
		hash, err := h.Chain.GetBestBlockHash()
		if err != nil {
			t.Fatal(err)
		}

		height, err := h.Chain.GetBestBlockHeight()
		if err != nil {
			t.Fatal(err)
		}
		hh, err := h.Chain.GetBlockHash(height)
		if err != nil {
			t.Fatal(err)
		}
		if hash != hh {
			time.Sleep(time.Millisecond * 100)
			continue
		}

		// we expect no next block
		_, err = h.Chain.GetBlock("", height+1)
		if err != nil {
			if err != bchain.ErrBlockNotFound {
				t.Error(err)
			}
			return
		}
	}
	t.Error("GetBestBlockHash() didn't get the best hash")
}
func testGetBestBlockHeight(t *testing.T, h *TestHandler) {
	for i := 0; i < 3; i++ {
		height, err := h.Chain.GetBestBlockHeight()
		if err != nil {
			t.Fatal(err)
		}

		// we expect no next block
		_, err = h.Chain.GetBlock("", height+1)
		if err != nil {
			if err != bchain.ErrBlockNotFound {
				t.Error(err)
			}
			return
		}
	}
	t.Error("GetBestBlockHeigh() didn't get the the best heigh")
}
func testGetBlockHeader(t *testing.T, h *TestHandler) {
	want := &bchain.BlockHeader{
		Hash:   h.TestData.BlockHash,
		Height: h.TestData.BlockHeight,
		Time:   h.TestData.BlockTime,
	}

	got, err := h.Chain.GetBlockHeader(h.TestData.BlockHash)
	if err != nil {
		t.Fatal(err)
	}

	// Confirmations is variable field, we just check if is set and reset it
	if got.Confirmations <= 0 {
		t.Fatalf("GetBlockHeader() got struct with invalid Confirmations field")
	}
	got.Confirmations = 0

	got.Prev, got.Next = "", ""

	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetBlockHeader() got=%+v, want=%+v", got, want)
	}
}

func getMempool(t *testing.T, h *TestHandler) []string {
	txs, err := h.Chain.GetMempool()
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) == 0 {
		t.Skip("Skipping test, mempool is empty")
	}

	return txs
}

func getMempoolAddresses(t *testing.T, h *TestHandler, txs []string) map[string][]string {
	txid2addrs := map[string][]string{}
	for i := 0; i < len(txs); i++ {
		tx, err := h.Chain.GetTransactionForMempool(txs[i])
		if err != nil {
			t.Fatal(err)
		}
		addrs := []string{}
		for _, vin := range tx.Vin {
			for _, a := range vin.Addresses {
				if isSearchableAddr(a) {
					addrs = append(addrs, a)
				}
			}
		}
		for _, vout := range tx.Vout {
			for _, a := range vout.ScriptPubKey.Addresses {
				if isSearchableAddr(a) {
					addrs = append(addrs, a)
				}
			}
		}
		if len(addrs) > 0 {
			txid2addrs[tx.Txid] = addrs
		}
	}
	return txid2addrs
}

func isSearchableAddr(addr string) bool {
	return len(addr) > 3 && addr[:3] != "OP_"
}

func intersect(a, b []string) []string {
	setA := mapset.NewSet()
	for _, v := range a {
		setA.Add(v)
	}
	setB := mapset.NewSet()
	for _, v := range b {
		setB.Add(v)
	}
	inter := setA.Intersect(setB)
	res := make([]string, 0, inter.Cardinality())
	for v := range inter.Iter() {
		res = append(res, v.(string))
	}
	return res
}

func containsString(slice []string, s string) bool {
	for i := 0; i < len(slice); i++ {
		if slice[i] == s {
			return true
		}
	}
	return false
}