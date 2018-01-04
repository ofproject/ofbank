
package state

import (
	"bytes"
	"fmt"
	"io"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

var emptyCodeHash = crypto.Keccak256(nil)
var CurBlknum int64 // Redbull current_block_header(number)

type Code []byte

func (self Code) String() string {
	return string(self) //strings.Join(Disassemble(self), " ")
}

type Storage map[common.Hash]common.Hash

func (self Storage) String() (str string) {
	for key, value := range self {
		str += fmt.Sprintf("%X : %X\n", key, value)
	}

	return
}

func (self Storage) Copy() Storage {
	cpy := make(Storage)
	for key, value := range self {
		cpy[key] = value
	}

	return cpy
}

// stateObject represents an Ethereum account which is being modified.
//
// The usage pattern is as follows:
// First you need to obtain a state object.
// Account values can be accessed and modified through the object.
// Finally, call CommitTrie to write the modified storage trie into a database.
type stateObject struct {
	address  common.Address
	addrHash common.Hash // hash of ethereum address of the account
	data     Account
	db       *StateDB

	// DB error.
	// State objects are used by the consensus core and VM which are
	// unable to deal with database-level errors. Any error that occurs
	// during a database read is memoized here and will eventually be returned
	// by StateDB.Commit.
	dbErr error

	// Write caches.
	trie Trie // storage trie, which becomes non-nil on first access
	code Code // contract bytecode, which gets set when code is loaded

	cachedStorage Storage // Storage entry cache to avoid duplicate reads
	dirtyStorage  Storage // Storage entries that need to be flushed to disk

	// Cache flags.
	// When an object is marked suicided it will be delete from the trie
	// during the "update" phase of the state transition.
	dirtyCode bool // true if the code was updated
	suicided  bool
	touched   bool
	deleted   bool
	onDirty   func(addr common.Address) // Callback method to mark a state object newly dirty
}

// empty returns whether the account is considered empty.
func (s *stateObject) empty() bool {
	sdb, _ := strconv.ParseFloat(s.data.Balance, 64)	// Water Cherry
	cbn, _ := strconv.ParseInt(s.data.LastCBN, 10,64)
	cag, _ := strconv.ParseFloat(s.data.Coinage, 64)
	return s.data.Nonce == 0 && big.NewFloat(cag).Sign() == 0 && big.NewInt(cbn).Sign() == 0 && big.NewFloat(sdb).Sign() == 0 && bytes.Equal(s.data.CodeHash, emptyCodeHash)
}

// Account is the Ethereum consensus representation of accounts.
// These objects are stored in the main account trie.
type Account struct {
	Nonce    uint64
	Balance  string	// Water Cherry was *big.Int
	Coinage	 string // Water Coke (want int64)
	LastCBN	 string // Water Coke (last coinage-block-number)
 	Root     common.Hash // merkle root of the storage trie
	CodeHash []byte
}

// newObject creates a state object.
func newObject(db *StateDB, address common.Address, data Account, onDirty func(addr common.Address)) *stateObject {
	if data.Balance == "" {
		data.Balance = "0.00" // was new(big.Int)
	}
	if data.Coinage == "" {	// Water Coke
		//ca, _ := strconv.ParseInt(data.Balance, 10, 64)
		data.Coinage = "0,00"//big.NewInt(0) //ca * 1E6
	}
	if data.LastCBN == "" {
		data.LastCBN = "0"//big.NewInt(0)	// should check per every 10k of blocks
	}
	if data.CodeHash == nil {
		data.CodeHash = emptyCodeHash
	}

	return &stateObject{
		db:            db,
		address:       address,
		addrHash:      crypto.Keccak256Hash(address[:]),
		data:          data,
		cachedStorage: make(Storage),
		dirtyStorage:  make(Storage),
		onDirty:       onDirty,
	}
}

// EncodeRLP implements rlp.Encoder.
func (c *stateObject) EncodeRLP(w io.Writer) error {
	return rlp.Encode(w, c.data)
}

// setError remembers the first non-nil error it is called with.
func (self *stateObject) setError(err error) {
	if self.dbErr == nil {
		self.dbErr = err
	}
}

func (self *stateObject) markSuicided() {
	self.suicided = true
	if self.onDirty != nil {
		self.onDirty(self.Address())
		self.onDirty = nil
	}
}

func (c *stateObject) touch() {
	c.db.journal = append(c.db.journal, touchChange{
		account:   &c.address,
		prev:      c.touched,
		prevDirty: c.onDirty == nil,
	})
	if c.onDirty != nil {
		c.onDirty(c.Address())
		c.onDirty = nil
	}
	c.touched = true
}

func (c *stateObject) getTrie(db Database) Trie {
	if c.trie == nil {
		var err error
		c.trie, err = db.OpenStorageTrie(c.addrHash, c.data.Root)
		if err != nil {
			c.trie, _ = db.OpenStorageTrie(c.addrHash, common.Hash{})
			c.setError(fmt.Errorf("can't create storage trie: %v", err))
		}
	}
	return c.trie
}

// GetState returns a value in account storage.
func (self *stateObject) GetState(db Database, key common.Hash) common.Hash {
	value, exists := self.cachedStorage[key]
	if exists {
		return value
	}
	// Load from DB in case it is missing.
	enc, err := self.getTrie(db).TryGet(key[:])
	if err != nil {
		self.setError(err)
		return common.Hash{}
	}
	if len(enc) > 0 {
		_, content, _, err := rlp.Split(enc)
		if err != nil {
			self.setError(err)
		}
		value.SetBytes(content)
	}
	if (value != common.Hash{}) {
		self.cachedStorage[key] = value
	}
fmt.Println("stateobj: ", value)  // Water Eout
	return value
}

// SetState updates a value in account storage.
func (self *stateObject) SetState(db Database, key, value common.Hash) {
	self.db.journal = append(self.db.journal, storageChange{
		account:  &self.address,
		key:      key,
		prevalue: self.GetState(db, key),
	})
	self.setState(key, value)
}

func (self *stateObject) setState(key, value common.Hash) {
	self.cachedStorage[key] = value
	self.dirtyStorage[key] = value

	if self.onDirty != nil {
		self.onDirty(self.Address())
		self.onDirty = nil
	}
}

// updateTrie writes cached storage modifications into the object's storage trie.
func (self *stateObject) updateTrie(db Database) Trie {
	tr := self.getTrie(db)
	for key, value := range self.dirtyStorage {
		delete(self.dirtyStorage, key)
		if (value == common.Hash{}) {
			self.setError(tr.TryDelete(key[:]))
			continue
		}
		// Encoding []byte cannot fail, ok to ignore the error.
		v, _ := rlp.EncodeToBytes(bytes.TrimLeft(value[:], "\x00"))
		self.setError(tr.TryUpdate(key[:], v))
	}
	return tr
}

// UpdateRoot sets the trie root to the current root hash of
func (self *stateObject) updateRoot(db Database) {
	self.updateTrie(db)
	self.data.Root = self.trie.Hash()
}

// CommitTrie the storage trie of the object to dwb.
// This updates the trie root.
func (self *stateObject) CommitTrie(db Database, dbw trie.DatabaseWriter) error {
	self.updateTrie(db)
	if self.dbErr != nil {
		return self.dbErr
	}
	root, err := self.trie.CommitTo(dbw)
	if err == nil {
		self.data.Root = root
	}
	return err
}

// AddBalance removes amount from c's balance.
// It is used to add funds to the destination account of a transfer.
func (c *stateObject) AddBalance(amount string) {	// was *big.Int
	// EIP158: We must check emptiness for the objects such that the account
	// clearing (0,0,0 objects) can take effect.
	ca, _ := strconv.ParseFloat(amount, 64)	// Water Cherry
	if big.NewFloat(ca).Sign() == 0 {
		if c.empty() {
			c.touch()
		}
		return
	}
	cb, _ := strconv.ParseFloat(c.Balance(), 64)
	c.SetBalance(cb + ca) // Water Cherry
}

// SubBalance removes amount from c's balance.
// It is used to remove funds from the origin account of a transfer.
func (c *stateObject) SubBalance(amount string) {
	ca, _ := strconv.ParseFloat(amount, 64)	// Water Cherry
	if big.NewFloat(ca).Sign() == 0 {
		return
	}
	cb, _ := strconv.ParseFloat(c.Balance(), 64)
	c.SetBalance(cb - ca) // Water Cherry
}

func (self *stateObject) SetBalance(amount float64) {	// was *big.Int
	self.db.journal = append(self.db.journal, balanceChange{
		account: &self.address,
		prev:    self.data.Balance,	// new(big.Int).Set(self.data.Balance),
	})

	self.setBalance(fmt.Sprintf("%#6.6f", amount))			// toString
}

func (self *stateObject) setBalance(amount string) {	// was *big.Int
	self.data.Balance = amount
	if self.onDirty != nil {
		self.onDirty(self.Address())
		self.onDirty = nil
	}
}

/* ------ COINAGE ------ ++++++++++++++++++++++++++++++++++++++++++++++++ */


func (age *stateObject) AddCoinage(cav string) {
	cga, _ := strconv.ParseFloat(cav, 64)	// Water Cherry
	if big.NewFloat(cga).Sign() == 0 {
		if age.empty() {
			age.touch()
		}
		return
	}
	oldcga, _ := strconv.ParseFloat(age.Coinage(), 64)
	age.SetCoinage(oldcga+cga)
}
/*
func (age *stateObject) SubCoinage(cav string) {
	caf, _ := strconv.ParseFloat(cav, 64)	// Water Coke
	if big.NewFloat(caf).Sign() == 0 {
		return
	}
	last_ca, _ := strconv.ParseFloat(age.Coinage(), 64)
	age.SetCoinage(int64(last_ca - caf))
}
*/

func (self *stateObject) SetCoinage(cav float64) {
	self.db.journal = append(self.db.journal, coinageChange{
		account: &self.address,
		prev:	 self.data.Coinage,
	})
	self.setCoinage(fmt.Sprintf("%#6.6f", cav))
}

func (self *stateObject) setCoinage(cav string) {		// Water Coke
	self.data.Coinage = cav
	if self.onDirty != nil {
		self.onDirty(self.Address())
		self.onDirty = nil
	}
}

func (self *stateObject) SetLastCBN(cbnv string) {
	cbn, _ := strconv.ParseInt(cbnv, 10,64)	// Water Cherry
	if big.NewInt(cbn).Sign() == 0 {
		if self.empty() {
			self.touch()
		}
		return
	}
	self.db.journal = append(self.db.journal, lastCBNChange{
		account: &self.address,
		prev:	self.data.LastCBN,
	})
	self.setLastCBN(cbnv)
}

func (self *stateObject) setLastCBN(cbnv string) {
	self.data.LastCBN = cbnv
	if self.onDirty != nil {
		self.onDirty(self.Address())
		self.onDirty = nil
	}
}

/* ------ COINAGE ------  ############################################### */

// Return the gas back to the origin. Used by the Virtual machine or Closures
func (c *stateObject) ReturnGas(gas *big.Int) {}

func (self *stateObject) deepCopy(db *StateDB, onDirty func(addr common.Address)) *stateObject {
	stateObject := newObject(db, self.address, self.data, onDirty)
	if self.trie != nil {
		stateObject.trie = db.db.CopyTrie(self.trie)
	}
	stateObject.code = self.code
	stateObject.dirtyStorage = self.dirtyStorage.Copy()
	stateObject.cachedStorage = self.dirtyStorage.Copy()
	stateObject.suicided = self.suicided
	stateObject.dirtyCode = self.dirtyCode
	stateObject.deleted = self.deleted
	return stateObject
}

//
// Attribute accessors
//

// Returns the address of the contract/account
func (c *stateObject) Address() common.Address {
	return c.address
}

// Code returns the contract code associated with this object, if any.
func (self *stateObject) Code(db Database) []byte {
	if self.code != nil {
		return self.code
	}
	if bytes.Equal(self.CodeHash(), emptyCodeHash) {
		return nil
	}
	code, err := db.ContractCode(self.addrHash, common.BytesToHash(self.CodeHash()))
	if err != nil {
		self.setError(fmt.Errorf("can't load code hash %x: %v", self.CodeHash(), err))
	}
	self.code = code
	return code
}

func (self *stateObject) SetCode(codeHash common.Hash, code []byte) {
	prevcode := self.Code(self.db.db)
	self.db.journal = append(self.db.journal, codeChange{
		account:  &self.address,
		prevhash: self.CodeHash(),
		prevcode: prevcode,
	})
	self.setCode(codeHash, code)
}

func (self *stateObject) setCode(codeHash common.Hash, code []byte) {
	self.code = code
	self.data.CodeHash = codeHash[:]
	self.dirtyCode = true
	if self.onDirty != nil {
		self.onDirty(self.Address())
		self.onDirty = nil
	}
}

func (self *stateObject) SetNonce(nonce uint64) {
	self.db.journal = append(self.db.journal, nonceChange{
		account: &self.address,
		prev:    self.data.Nonce,
	})
	self.setNonce(nonce)
}

func (self *stateObject) setNonce(nonce uint64) {
	self.data.Nonce = nonce
	if self.onDirty != nil {
		self.onDirty(self.Address())
		self.onDirty = nil
	}
}

func (self *stateObject) CodeHash() []byte {
	return self.data.CodeHash
}

func (self *stateObject) Balance() string {	// *big.Int	Water Cherry
	return self.data.Balance
}

func (self *stateObject) Coinage() string {	// int64 Water Coke
	return self.data.Coinage
}

func (self *stateObject) LastCBN() string {	// int64 Water Coke
	return self.data.LastCBN
}

func (self *stateObject) Nonce() uint64 {
	return self.data.Nonce
}

// Never called, but must be present to allow stateObject to be used
// as a vm.Account interface that also satisfies the vm.ContractRef
// interface. Interfaces are awesome.
func (self *stateObject) Value() *big.Int {
	panic("Value on stateObject should never be called")
}
