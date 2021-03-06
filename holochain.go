// Copyright (C) 2013-2017, The MetaCurrency Project (Eric Harris-Braun, Arthur Brock, et. al.)
// Use of this source code is governed by GPLv3 found in the LICENSE file
//---------------------------------------------------------------------------------------

// Holochains are a distributed data store: DHT tightly bound to signed hash chains
// for provenance and data integrity.
package holochain

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/google/uuid"
	ic "github.com/libp2p/go-libp2p-crypto"
	peer "github.com/libp2p/go-libp2p-peer"
	mh "github.com/multiformats/go-multihash"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const Version int = 3
const VersionStr string = "3"

// AgentEntry structure for building KeyEntryType entries
type AgentEntry struct {
	Name    AgentName
	KeyType KeytypeType
	Key     []byte // marshaled public key
}

// Zome struct encapsulates logically related code, from "chromosome"
type Zome struct {
	Name        string
	Description string
	Code        string // file name of DNA code
	CodeHash    Hash
	Entries     map[string]EntryDef
	NucleusType string
}

// Loggers holds the logging structures for the different parts of the system
type Loggers struct {
	App        Logger
	DHT        Logger
	Gossip     Logger
	TestPassed Logger
	TestFailed Logger
	TestInfo   Logger
}

// Config holds the non-DNA configuration for a holo-chain
type Config struct {
	Port            int
	PeerModeAuthor  bool
	PeerModeDHTNode bool
	BootstrapServer string
	Loggers         Loggers
}

// Holochain struct holds the full "DNA" of the holochain
type Holochain struct {
	Version          int
	Id               uuid.UUID
	Name             string
	Properties       map[string]string
	PropertiesSchema string
	HashType         string
	BasedOn          Hash // holochain hash for base schemas and code
	Zomes            map[string]*Zome
	//---- private values not serialized; initialized on Load
	id             peer.ID // this is hash of the id, also used in the node
	dnaHash        Hash
	agentHash      Hash
	path           string
	agent          Agent
	encodingFormat string
	hashSpec       HashSpec
	config         Config
	dht            *DHT
	node           *Node
	chain          *Chain // the chain itself
}

var debugLog Logger
var infoLog Logger

func Debug(m string) {
	debugLog.Log(m)
}

func Debugf(m string, args ...interface{}) {
	debugLog.Logf(m, args...)
}

func Info(m string) {
	infoLog.Log(m)
}

func Infof(m string, args ...interface{}) {
	infoLog.Logf(m, args...)
}

// Register function that must be called once at startup by any client app
func Register() {
	gob.Register(Header{})
	gob.Register(AgentEntry{})
	gob.Register(Hash{})
	gob.Register(PutReq{})
	gob.Register(GetReq{})
	gob.Register(MetaReq{})
	gob.Register(MetaQuery{})
	gob.Register(GossipReq{})
	gob.Register(Gossip{})
	gob.Register(ValidateResponse{})
	gob.Register(Put{})
	gob.Register(GobEntry{})
	gob.Register(MetaQueryResp{})
	gob.Register(MetaEntry{})

	RegisterBultinNucleii()
	RegisterBultinPersisters()

	infoLog.New(nil)
	debugLog.New(nil)

	rand.Seed(time.Now().Unix()) // initialize global pseudo random generator
}

func findDNA(path string) (f string, err error) {
	p := path + "/" + DNAFileName
	matches, err := filepath.Glob(p + ".*")
	if err != nil {
		return
	}
	for _, fn := range matches {
		s := strings.Split(fn, ".")
		f = s[len(s)-1]
		if f == "json" || f == "yaml" || f == "toml" {
			break
		}
		f = ""
	}

	if f == "" {
		err = errors.New("DNA not found")
		return
	}
	return
}

// IsConfigured checks a directory for correctly set up holochain configuration files
func (s *Service) IsConfigured(name string) (f string, err error) {
	path := s.Path + "/" + name

	f, err = findDNA(path)
	if err != nil {
		return
	}

	/*	// found a format now check that there's a store
		p := path + "/" + StoreFileName + ".db"
		if !fileExists(p) {
			err = errors.New("chain store missing: " + p)
			return
		}
	*/
	return
}

// Load instantiates a Holochain instance
func (s *Service) Load(name string) (h *Holochain, err error) {
	f, err := s.IsConfigured(name)
	if err != nil {
		return
	}
	h, err = s.load(name, f)
	return
}

// NewHolochain creates a new holochain structure with a randomly generated ID and default values
func NewHolochain(agent Agent, path string, format string, zomes ...Zome) Holochain {
	u, err := uuid.NewUUID()
	if err != nil {
		panic(err)
	}
	h := Holochain{
		Id:             u,
		HashType:       "sha2-256",
		agent:          agent,
		path:           path,
		encodingFormat: format,
	}

	// once the agent is set up we can calculate the id
	h.id, err = peer.IDFromPrivateKey(agent.PrivKey())
	if err != nil {
		panic(err)
	}

	h.PrepareHashType()
	h.Zomes = make(map[string]*Zome)
	for i := range zomes {
		z := zomes[i]
		h.Zomes[z.Name] = &z
	}

	return h
}

// DecodeDNA decodes a Holochain structure from an io.Reader
func DecodeDNA(reader io.Reader, format string) (hP *Holochain, err error) {
	var h Holochain
	err = Decode(reader, format, &h)
	if err != nil {
		return
	}
	hP = &h
	hP.encodingFormat = format

	return
}

// load unmarshals a holochain structure for the named chain and format
func (s *Service) load(name string, format string) (hP *Holochain, err error) {

	path := s.Path + "/" + name
	var f *os.File
	f, err = os.Open(path + "/" + DNAFileName + "." + format)
	if err != nil {
		return
	}
	defer f.Close()
	h, err := DecodeDNA(f, format)
	if err != nil {
		return
	}
	h.path = path
	h.encodingFormat = format

	// load the config
	f, err = os.Open(path + "/" + ConfigFileName + "." + format)
	if err != nil {
		return
	}
	defer f.Close()
	err = Decode(f, format, &h.config)
	if err != nil {
		return
	}
	if err = h.setupConfig(); err != nil {
		return
	}

	// try and get the agent from the holochain instance
	agent, err := LoadAgent(path)
	if err != nil {
		// get the default if not available
		agent, err = LoadAgent(filepath.Dir(path))
	}
	if err != nil {
		return
	}
	h.agent = agent

	// once the agent is set up we can calculate the id
	h.id, err = peer.IDFromPrivateKey(agent.PrivKey())
	if err != nil {
		return
	}

	/*	h.store, err = CreatePersister(BoltPersisterName, path+"/"+StoreFileName+".db")
		if err != nil {
			return
		}

		err = h.store.Init()
		if err != nil {
			return
		}
	*/
	if err = h.PrepareHashType(); err != nil {
		return
	}

	h.chain, err = NewChainFromFile(h.hashSpec, path+"/"+StoreFileName+".dat")
	if err != nil {
		return
	}

	// if the chain has been started there should be a DNAHashFile which
	// we can load to check against the actual hash of the DNA entry
	var b []byte
	b, err = readFile(h.path, DNAHashFileName)
	if err == nil {
		h.dnaHash, err = NewHash(string(b))
		if err != nil {
			return
		}
		// @TODO compare value from file to actual hash
	}

	if h.chain.Length() > 0 {
		h.agentHash = h.chain.Headers[1].EntryLink
	}
	if err = h.Prepare(); err != nil {
		return
	}

	hP = h
	return
}

// Agent exposes the agent element
func (h *Holochain) Agent() Agent {
	return h.agent
}

// PrepareHashType makes sure the given string is a correct multi-hash and stores
// the code and length to the Holochain struct
func (h *Holochain) PrepareHashType() (err error) {
	if c, ok := mh.Names[h.HashType]; !ok {
		return fmt.Errorf("Unknown hash type: %s", h.HashType)
	} else {
		h.hashSpec.Code = c
		h.hashSpec.Length = -1
	}

	return
}

// Prepare sets up a holochain to run by:
// validating the DNA, loading the schema validators, setting up a Network node and setting up the DHT
func (h *Holochain) Prepare() (err error) {

	if err = h.PrepareHashType(); err != nil {
		return
	}
	for zomeType, z := range h.Zomes {
		var n Nucleus
		n, err = h.MakeNucleus(zomeType)
		if err != nil {
			return
		}
		if err = n.ChainRequires(); err != nil {
			return
		}

		if !fileExists(h.path + "/" + z.Code) {
			return errors.New("DNA specified code file missing: " + z.Code)
		}
		for k := range z.Entries {
			e := z.Entries[k]
			sc := e.Schema
			if sc != "" {
				if !fileExists(h.path + "/" + sc) {
					return errors.New("DNA specified schema file missing: " + sc)
				} else {
					if strings.HasSuffix(sc, ".json") {
						if err = e.BuildJSONSchemaValidator(h.path); err != nil {
							return err
						}
						z.Entries[k] = e
					}
				}
			}
		}
	}

	h.dht = NewDHT(h)

	return
}

// Activate fires up the holochain node
func (h *Holochain) Activate() (err error) {
	listenaddr := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", h.config.Port)
	h.node, err = NewNode(listenaddr, h.id, h.Agent().PrivKey())
	if err != nil {
		return
	}

	if h.config.PeerModeDHTNode {
		if err = h.dht.StartDHT(); err != nil {
			return
		}
		e := h.BSpost()
		if e != nil {
			h.dht.dlog.Logf("error in BSpost: %s", e.Error())
		}
		e = h.BSget()
		if e != nil {
			h.dht.dlog.Logf("error in BSget: %s", e.Error())
		}
	}
	if h.config.PeerModeAuthor {
		if err = h.node.StartSrc(h); err != nil {
			return
		}
	}
	return
}

/*
// getMetaHash gets a value from the store that's a hash
func (h *Holochain) getMetaHash(key string) (hash Hash, err error) {
	v, err := h.store.GetMeta(key)
	if err != nil {
		return
	}
	hash.H = v
	if v == nil {
		err = mkErr("Meta key '" + key + "' uninitialized")
	}
	return
}
*/

// Path returns a holochain path
func (h *Holochain) Path() string {
	return h.path
}

// DNAHash returns the hash of the DNA entry which is also the holochain ID
func (h *Holochain) DNAHash() (id Hash) {
	return h.dnaHash.Clone()
}

// AgentHash returns the hash of the Agent entry
func (h *Holochain) Agenthash() (id Hash) {
	return h.agentHash.Clone()
}

// Top returns a hash of top header or err if not yet defined
func (h *Holochain) Top() (top Hash, err error) {
	tp := h.chain.Hashes[len(h.chain.Hashes)-1]
	top = tp.Clone()
	return
}

// Started returns true if the chain has been gened
func (h *Holochain) Started() bool {
	return h.DNAHash().String() != ""
}

// GenChain establishes a holochain instance by creating the initial genesis entries in the chain
// It assumes a properly set up .holochain sub-directory with a config file and
// keys for signing.  See GenDev()
func (h *Holochain) GenChain() (headerHash Hash, err error) {

	if h.Started() {
		err = mkErr("chain already started")
		return
	}

	defer func() {
		if err != nil {
			panic("cleanup after failed gen not implemented!  Error was: " + err.Error())
		}
	}()

	if err = h.Prepare(); err != nil {
		return
	}

	var buf bytes.Buffer
	err = h.EncodeDNA(&buf)

	e := GobEntry{C: buf.Bytes()}

	var dnaHeader *Header
	_, dnaHeader, err = h.NewEntry(time.Now(), DNAEntryType, &e)
	if err != nil {
		return
	}

	h.dnaHash = dnaHeader.EntryLink.Clone()

	var k AgentEntry
	k.Name = h.agent.Name()
	k.KeyType = h.agent.KeyType()

	pk := h.agent.PrivKey().GetPublic()

	k.Key, err = ic.MarshalPublicKey(pk)
	if err != nil {
		return
	}

	e.C = k
	var agentHeader *Header
	headerHash, agentHeader, err = h.NewEntry(time.Now(), AgentEntryType, &e)
	if err != nil {
		return
	}

	h.agentHash = agentHeader.EntryLink

	if err = writeFile(h.path, DNAHashFileName, []byte(h.dnaHash.String())); err != nil {
		return
	}

	/*
		err = h.store.PutMeta(IDMetaKey, dnaHeader.EntryLink.H)
		if err != nil {
			return
		}
	*/
	err = h.dht.SetupDHT()
	if err != nil {
		return
	}

	// run the init functions of each zome
	for zomeName, z := range h.Zomes {
		var n Nucleus
		n, err = h.makeNucleus(z)
		if err == nil {
			err = n.ChainGenesis()
			if err != nil {
				err = fmt.Errorf("In '%s' zome: %s", zomeName, err.Error())
				return
			}
		}
	}

	return
}

// Clone copies DNA files from a source
func (s *Service) Clone(srcPath string, path string, new bool) (hP *Holochain, err error) {
	hP, err = gen(path, func(path string) (hP *Holochain, err error) {

		format, err := findDNA(srcPath)
		if err != nil {
			return
		}

		f, err := os.Open(srcPath + "/" + DNAFileName + "." + format)
		if err != nil {
			return
		}
		defer f.Close()
		h, err := DecodeDNA(f, format)
		if err != nil {
			return
		}

		agent, err := LoadAgent(filepath.Dir(path))
		if err != nil {
			return
		}
		h.path = path
		h.agent = agent

		// once the agent is set up we can calculate the id
		h.id, err = peer.IDFromPrivateKey(agent.PrivKey())
		if err != nil {
			return
		}

		// make a config file
		if err = makeConfig(h, s); err != nil {
			return
		}

		if new {
			// generate a new UUID
			var u uuid.UUID
			u, err = uuid.NewUUID()
			if err != nil {
				return
			}
			h.Id = u

			// use the path as the name
			h.Name = filepath.Base(path)
		}

		if err = CopyDir(srcPath+"/ui", path+"/ui"); err != nil {
			return
		}

		if err = CopyFile(srcPath+"/schema_properties.json", path+"/schema_properties.json"); err != nil {
			return
		}

		if dirExists(srcPath + "/test") {
			if err = CopyDir(srcPath+"/test", path+"/test"); err != nil {
				return
			}
		}

		for _, z := range h.Zomes {
			var bs []byte
			bs, err = readFile(srcPath, z.Code)
			if err != nil {
				return
			}
			if err = writeFile(path, z.Code, bs); err != nil {
				return
			}
			for k := range z.Entries {
				e := z.Entries[k]
				sc := e.Schema
				if sc != "" {
					if err = CopyFile(srcPath+"/"+sc, path+"/"+sc); err != nil {
						return
					}
				}
			}
		}

		hP = h
		return
	})
	return
}

// TestData holds a test entry for a chain
type TestData struct {
	Zome   string
	FnName string
	Input  string
	Output string
	Err    string
	Regexp string
}

func (h *Holochain) setupConfig() (err error) {
	if err = h.config.Loggers.App.New(nil); err != nil {
		return
	}
	if err = h.config.Loggers.DHT.New(nil); err != nil {
		return
	}
	if err = h.config.Loggers.Gossip.New(nil); err != nil {
		return
	}
	if err = h.config.Loggers.TestPassed.New(nil); err != nil {
		return
	}
	if err = h.config.Loggers.TestFailed.New(nil); err != nil {
		return
	}
	if err = h.config.Loggers.TestInfo.New(nil); err != nil {
		return
	}
	return
}

func makeConfig(h *Holochain, s *Service) (err error) {
	h.config = Config{
		Port:            DefaultPort,
		PeerModeDHTNode: s.Settings.DefaultPeerModeDHTNode,
		PeerModeAuthor:  s.Settings.DefaultPeerModeAuthor,
		BootstrapServer: s.Settings.DefaultBootstrapServer,
		Loggers: Loggers{
			App:        Logger{Format: "%{color:cyan}%{message}", Enabled: true},
			DHT:        Logger{Format: "%{color:yellow}%{time} DHT: %{message}"},
			Gossip:     Logger{Format: "%{color:blue}%{time} Gossip: %{message}"},
			TestPassed: Logger{Format: "%{color:green}%{message}", Enabled: true},
			TestFailed: Logger{Format: "%{color:red}%{message}", Enabled: true},
			TestInfo:   Logger{Format: "%{message}", Enabled: true},
		},
	}

	p := h.path + "/" + ConfigFileName + "." + h.encodingFormat
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()

	if err = Encode(f, h.encodingFormat, &h.config); err != nil {
		return
	}
	if err = h.setupConfig(); err != nil {
		return
	}
	return
}

// GenDev generates starter holochain DNA files from which to develop a chain
func (s *Service) GenDev(path string, format string) (hP *Holochain, err error) {
	hP, err = gen(path, func(path string) (hP *Holochain, err error) {
		agent, err := LoadAgent(filepath.Dir(path))
		if err != nil {
			return
		}

		zomes := []Zome{
			{Name: "myZome",
				Description: "this is a zygomas test zome",
				NucleusType: ZygoNucleusType,
				Entries: map[string]EntryDef{
					"myData":  {Name: "myData", DataFormat: DataFormatRawZygo},
					"primes":  {Name: "primes", DataFormat: DataFormatJSON},
					"profile": {Name: "profile", DataFormat: DataFormatJSON, Schema: "schema_profile.json"},
				},
			},
			{Name: "jsZome",
				Description: "this is a javascript test zome",
				NucleusType: JSNucleusType,
				Entries: map[string]EntryDef{
					"myOdds":  {Name: "myOdds", DataFormat: DataFormatRawJS},
					"profile": {Name: "profile", DataFormat: DataFormatJSON, Schema: "schema_profile.json"},
				},
			},
		}

		h := NewHolochain(agent, path, format, zomes...)

		// use the path as the name
		h.Name = filepath.Base(path)

		if err = makeConfig(&h, s); err != nil {
			return
		}

		schema := `{
	"title": "Properties Schema",
	"type": "object",
	"properties": {
		"description": {
			"type": "string"
		},
		"language": {
			"type": "string"
		}
	}
}`
		if err = writeFile(path, "schema_properties.json", []byte(schema)); err != nil {
			return
		}

		h.PropertiesSchema = "schema_properties.json"
		h.Properties = map[string]string{
			"description": "a bogus test holochain",
			"language":    "en"}

		schema = `{
	"title": "Profile Schema",
	"type": "object",
	"properties": {
		"firstName": {
			"type": "string"
		},
		"lastName": {
			"type": "string"
		},
		"age": {
			"description": "Age in years",
			"type": "integer",
			"minimum": 0
		}
	},
	"required": ["firstName", "lastName"]
}`
		if err = writeFile(path, "schema_profile.json", []byte(schema)); err != nil {
			return
		}

		fixtures := [7]TestData{
			{
				Zome:   "myZome",
				FnName: "addData",
				Input:  "2",
				Output: "%h%"},
			{
				Zome:   "myZome",
				FnName: "addData",
				Input:  "4",
				Output: "%h%"},
			{
				Zome:   "myZome",
				FnName: "addData",
				Input:  "5",
				Err:    "Error calling 'commit': Invalid entry: 5"},
			{
				Zome:   "myZome",
				FnName: "addPrime",
				Input:  "{\"prime\":7}",
				Output: "\"%h%\""}, // quoted because return value is json
			{
				Zome:   "myZome",
				FnName: "addPrime",
				Input:  "{\"prime\":4}",
				Err:    `Error calling 'commit': Invalid entry: {"Atype":"hash", "prime":4, "zKeyOrder":["prime"]}`},
			{
				Zome:   "jsZome",
				FnName: "addProfile",
				Input:  `{"firstName":"Art","lastName":"Brock"}`,
				Output: `"%h%"`},
			{
				Zome:   "myZome",
				FnName: "getDNA",
				Input:  "",
				Output: "%dna%"},
		}

		fixtures2 := [2]TestData{
			{
				Zome:   "jsZome",
				FnName: "addOdd",
				Input:  "7",
				Output: "%h%"},
			{
				Zome:   "jsZome",
				FnName: "addOdd",
				Input:  "2",
				Err:    "Invalid entry: 2"},
		}

		uiPath := path + "/ui"
		if err = os.MkdirAll(uiPath, os.ModePerm); err != nil {
			return nil, err
		}
		for fileName, fileText := range SampleUI {
			if err = writeFile(uiPath, fileName, []byte(fileText)); err != nil {
				return
			}
		}

		code := make(map[string]string)
		code["myZome"] = `
(expose "getDNA" STRING)
(defn getDNA [x] App_DNAHash)
(expose "exposedfn" STRING)
(defn exposedfn [x] (concat "result: " x))
(expose "addData" STRING)
(defn addData [x] (commit "myData" x))
(expose "addPrime" JSON)
(defn addPrime [x] (commit "primes" x))
(defn validate [entryType entry props]
  (cond (== entryType "myData")  (cond (== (mod entry 2) 0) true false)
        (== entryType "primes")  (isprime (hget entry %prime))
        (== entryType "profile") true
        false)
)
(defn genesis [] true)
`
		code["jsZome"] = `
expose("getProperty",HC.STRING);
function getProperty(x) {return property(x)};
expose("addOdd",HC.STRING);
function addOdd(x) {return commit("myOdds",x);}
expose("addProfile",HC.JSON);
function addProfile(x) {return commit("profile",x);}
function validate(entry_type,entry,props) {
if (entry_type=="myOdds") {
  return entry%2 != 0
}
if (entry_type=="profile") {
  return true
}
return false
}
function genesis() {return true}
`

		testPath := path + "/test"
		if err = os.MkdirAll(testPath, os.ModePerm); err != nil {
			return nil, err
		}

		for n := range h.Zomes {
			z, _ := h.Zomes[n]
			switch z.NucleusType {
			case JSNucleusType:
				z.Code = fmt.Sprintf("zome_%s.js", z.Name)
			case ZygoNucleusType:
				z.Code = fmt.Sprintf("zome_%s.zy", z.Name)
			default:
				err = fmt.Errorf("unknown nucleus type:%s", z.NucleusType)
				return
			}

			c, _ := code[z.Name]
			if err = writeFile(path, z.Code, []byte(c)); err != nil {
				return
			}
		}

		// write out the tests
		for i, d := range fixtures {
			fn := fmt.Sprintf("test_%d.json", i)
			var j []byte
			t := []TestData{d}
			j, err = json.Marshal(t)
			if err != nil {
				return
			}
			if err = writeFile(testPath, fn, j); err != nil {
				return
			}
		}

		// also write out some grouped tests
		fn := "grouped.json"
		var j []byte
		j, err = json.Marshal(fixtures2)
		if err != nil {
			return
		}
		if err = writeFile(testPath, fn, j); err != nil {
			return
		}
		hP = &h
		return
	})
	return
}

// gen calls a make function which should build the holochain structure and supporting files
func gen(path string, makeH func(path string) (hP *Holochain, err error)) (h *Holochain, err error) {
	if dirExists(path) {
		return nil, mkErr(path + " already exists")
	}
	if err := os.MkdirAll(path, os.ModePerm); err != nil {
		return nil, err
	}

	// cleanup the directory if we enounter an error while generating
	defer func() {
		if err != nil {
			os.RemoveAll(path)
		}
	}()

	h, err = makeH(path)
	if err != nil {
		return
	}

	h.chain, err = NewChainFromFile(h.hashSpec, path+"/"+StoreFileName+".dat")
	if err != nil {
		return
	}

	/*
		h.store, err = CreatePersister(BoltPersisterName, path+"/"+StoreFileName+".db")
		if err != nil {
			return
		}

		err = h.store.Init()
		if err != nil {
			return
		}
	*/
	err = h.SaveDNA(false)
	if err != nil {
		return
	}

	return
}

// EncodeDNA encodes a holochain's DNA to an io.Writer
func (h *Holochain) EncodeDNA(writer io.Writer) (err error) {
	return Encode(writer, h.encodingFormat, &h)
}

// SaveDNA writes the holochain DNA to a file
func (h *Holochain) SaveDNA(overwrite bool) (err error) {
	p := h.path + "/" + DNAFileName + "." + h.encodingFormat
	if !overwrite && fileExists(p) {
		return mkErr(p + " already exists")
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	err = h.EncodeDNA(f)
	return
}

// GenDNAHashes generates hashes for all the definition files in the DNA.
// This function should only be called by developer tools at the end of the process
// of finalizing DNA development or versioning
func (h *Holochain) GenDNAHashes() (err error) {
	var b []byte
	for _, z := range h.Zomes {
		code := z.Code
		b, err = readFile(h.path, code)
		if err != nil {
			return
		}
		err = z.CodeHash.Sum(h.hashSpec, b)
		if err != nil {
			return
		}
		for i, e := range z.Entries {
			sc := e.Schema
			if sc != "" {
				b, err = readFile(h.path, sc)
				if err != nil {
					return
				}
				err = e.SchemaHash.Sum(h.hashSpec, b)
				if err != nil {
					return
				}
				z.Entries[i] = e
			}
		}

	}
	err = h.SaveDNA(true)
	return
}

// NewEntry adds an entry and it's header to the chain and returns the header and it's hash
func (h *Holochain) NewEntry(now time.Time, entryType string, entry Entry) (hash Hash, header *Header, err error) {

	var l int
	l, hash, header, err = h.chain.PrepareHeader(h.hashSpec, now, entryType, entry, h.agent.PrivKey())
	if err == nil {
		err = h.chain.addEntry(l, hash, header, entry)
	}
	/*
		// get the current top of the chain
		ph, err := h.Top()
		if err != nil {
			ph = NullHash()
		}

		// and also the the top entry of this type
		pth, err := h.TopType(t)
		if err != nil {
			pth = NullHash()
		}

		hash, header, err = newHeader(h.hashSpec, now, t, entry, h.agent.PrivKey(), ph, pth)
		if err != nil {
			return
		}

		// @TODO
		// we have to do this stuff because currently we are persisting immediatly.
		// instead we should be persisting from the Chain object.

		// encode the header for saving
		b, err := header.Marshal()
		if err != nil {
			return
		}
		// encode the entry into bytes
		m, err := entry.Marshal()
		if err != nil {
			return
		}

		err = h.store.Put(t, hash, b, header.EntryLink, m)
	*/
	return
}

// get low level access to entries/headers (only works inside a bolt transaction)
func get(hb *bolt.Bucket, eb *bolt.Bucket, key []byte, getEntry bool) (header Header, entry interface{}, err error) {
	v := hb.Get(key)

	err = header.Unmarshal(v, 34)
	if err != nil {
		return
	}
	if getEntry {
		v = eb.Get(header.EntryLink.H)
		var g GobEntry
		err = g.Unmarshal(v)
		if err != nil {
			return
		}
		entry = g.C
	}
	return
}

//func(key *Hash, h *Header, entry interface{}) error
func (h *Holochain) Walk(fn WalkerFn, entriesToo bool) (err error) {
	err = h.chain.Walk(fn)
	return
}

// Validate scans back through a chain to the beginning confirming that the last header points to DNA
// This is actually kind of bogus on your own chain, because theoretically you put it there!  But
// if the holochain file was copied from somewhere you can consider this a self-check
func (h *Holochain) Validate(entriesToo bool) (valid bool, err error) {

	err = h.Walk(func(key *Hash, header *Header, entry Entry) (err error) {
		// confirm the correctness of the header hash

		var bH Hash
		bH, _, err = header.Sum(h.hashSpec)
		if err != nil {
			return
		}

		if !bH.Equal(key) {
			return errors.New("header hash doesn't match")
		}

		// @TODO check entry hashes Etoo if entriesToo set
		if entriesToo {

		}
		return nil
	}, entriesToo)
	if err == nil {
		valid = true
	}
	return
}

// GetEntryDef returns an EntryDef of the given name
func (h *Holochain) GetEntryDef(t string) (zome *Zome, d *EntryDef, err error) {
	for _, z := range h.Zomes {
		e, ok := z.Entries[t]
		if ok {
			zome = z
			d = &e
			break
		}
	}
	if d == nil {
		err = errors.New("no definition for entry type: " + t)
	}
	return
}

// ValidateEntry passes an entry data to the chain's validation routine
// If the entry is valid err will be nil, otherwise it will contain some information about why the validation failed (or, possibly, some other system error)
func (h *Holochain) ValidateEntry(entryType string, entry Entry, props *ValidationProps) (err error) {

	if entry == nil {
		return errors.New("nil entry invalid")
	}

	z, d, err := h.GetEntryDef(entryType)
	if err != nil {
		return
	}

	// see if there is a schema validator for the entry type and validate it if so
	if d.validator != nil {
		var input interface{}
		if d.DataFormat == DataFormatJSON {
			if err = json.Unmarshal([]byte(entry.Content().(string)), &input); err != nil {
				return
			}
		} else {
			input = entry
		}
		Debugf("Validating %v against schema", input)
		if err = d.validator.Validate(input); err != nil {
			return
		}
	}

	// then run the nucleus (ie. "app" specific) validation rules
	n, err := h.makeNucleus(z)
	if err != nil {
		return
	}
	err = n.ValidateEntry(d, entry, props)
	return
}

// Call executes an exposed function
func (h *Holochain) Call(zomeType string, function string, arguments interface{}) (result interface{}, err error) {
	n, err := h.MakeNucleus(zomeType)
	if err != nil {
		return
	}
	result, err = n.Call(function, arguments)
	return
}

// MakeNucleus creates a Nucleus object based on the zome type
func (h *Holochain) MakeNucleus(t string) (n Nucleus, err error) {
	z, ok := h.Zomes[t]
	if !ok {
		err = errors.New("unknown zome: " + t)
		return
	}
	n, err = h.makeNucleus(z)
	return
}

func (h *Holochain) makeNucleus(z *Zome) (n Nucleus, err error) {
	var code []byte
	code, err = readFile(h.path, z.Code)
	if err != nil {
		return
	}
	n, err = CreateNucleus(h, z.NucleusType, string(code))
	return
}

func LoadTestData(path string) (map[string][]TestData, error) {
	files, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return nil, errors.New("no test data found in: " + path + "/test")
	}

	re := regexp.MustCompile(`(.*)\.json`)
	var tests = make(map[string][]TestData)
	for _, f := range files {
		if f.Mode().IsRegular() {
			x := re.FindStringSubmatch(f.Name())
			if len(x) > 0 {
				name := x[1]

				var v []byte
				v, err = readFile(path, x[0])
				if err != nil {
					return nil, err
				}
				var t []TestData
				err = json.Unmarshal(v, &t)
				if err != nil {
					return nil, err
				}
				tests[name] = t
			}
		}
	}
	return tests, err
}

func ToString(input interface{}) string {
	// @TODO this should probably act according the function schema
	// not just the return value
	var output string
	switch t := input.(type) {
	case []byte:
		output = string(t)
	case string:
		output = t
	default:
		output = fmt.Sprintf("%v", t)
	}
	return output
}

func (h *Holochain) TestStringReplacements(input, r1, r2, r3 string) string {
	// get the top hash for substituting for %h% in the test expectation
	top := h.chain.Top().EntryLink

	var output string
	output = strings.Replace(input, "%h%", top.String(), -1)
	output = strings.Replace(output, "%r1%", r1, -1)
	output = strings.Replace(output, "%r2%", r2, -1)
	output = strings.Replace(output, "%r3%", r3, -1)
	output = strings.Replace(output, "%dna%", h.dnaHash.String(), -1)
	output = strings.Replace(output, "%agent%", h.agentHash.String(), -1)
	output = strings.Replace(output, "%agentstr%", string(h.Agent().Name()), -1)
	output = strings.Replace(output, "%key%", peer.IDB58Encode(h.id), -1)
	return output
}

// Test loops through each of the test files calling the functions specified
// This function is useful only in the context of developing a holochain and will return
// an error if the chain has already been started (i.e. has genesis entries)
func (h *Holochain) Test() []error {
	info := h.config.Loggers.TestInfo
	passed := h.config.Loggers.TestPassed
	failed := h.config.Loggers.TestFailed

	var err error
	var errs []error
	if h.Started() {
		err = errors.New("chain already started")
		return []error{err}
	}

	// load up the test files into the tests array
	var tests, errorLoad = LoadTestData(h.path + "/test")
	if errorLoad != nil {
		return []error{errorLoad}
	}

	var lastResults [3]interface{}
	for name, ts := range tests {
		info.p("========================================")
		info.pf("Test: '%s' starting...", name)
		info.p("========================================")
		// setup the genesis entries
		err = h.Reset()
		_, err = h.GenChain()
		if err != nil {
			panic("gen err " + err.Error())
		}
		go h.dht.HandlePutReqs()
		for i, t := range ts {
			Debugf("------------------------------")
			info.pf("Test '%s' line %d: %s", name, i, t)
			time.Sleep(time.Millisecond * 10)
			if err == nil {
				testID := fmt.Sprintf("%s:%d", name, i)
				input := t.Input
				Debugf("Input before replacement: %s", input)
				r1 := strings.Trim(fmt.Sprintf("%v", lastResults[0]), "\"")
				r2 := strings.Trim(fmt.Sprintf("%v", lastResults[1]), "\"")
				r3 := strings.Trim(fmt.Sprintf("%v", lastResults[2]), "\"")
				input = h.TestStringReplacements(input, r1, r2, r3)
				Debugf("Input after replacement: %s", input)
				//====================
				var actualResult, actualError = h.Call(t.Zome, t.FnName, input)
				var expectedResult, expectedError = t.Output, t.Err
				var expectedResultRegexp = t.Regexp
				//====================
				lastResults[2] = lastResults[1]
				lastResults[1] = lastResults[0]
				lastResults[0] = actualResult
				if expectedError != "" {
					comparisonString := fmt.Sprintf("\nTest: %s\n\tExpected error:\t%v\n\tGot error:\t\t%v", testID, expectedError, actualError)
					if actualError == nil || (actualError.Error() != expectedError) {
						failed.pf("\n=====================\n%s\n\tfailed! m(\n=====================", comparisonString)
						err = fmt.Errorf(expectedError)
					} else {
						// all fine
						Debugf("%s\n\tpassed :D", comparisonString)
						err = nil
					}
				} else {
					if actualError != nil {
						errorString := fmt.Sprintf("\nTest: %s\n\tExpected:\t%s\n\tGot Error:\t\t%s\n", testID, expectedResult, actualError)
						err = fmt.Errorf(errorString)
						failed.pf(fmt.Sprintf("\n=====================\n%s\n\tfailed! m(\n=====================", errorString))
					} else {
						var resultString = ToString(actualResult)
						var match bool
						var comparisonString string
						if expectedResultRegexp != "" {
							Debugf("Test %s matching against regexp...", testID)
							expectedResultRegexp = h.TestStringReplacements(expectedResultRegexp, r1, r2, r3)
							comparisonString = fmt.Sprintf("\nTest: %s\n\tExpected regexp:\t%v\n\tGot:\t\t%v", testID, expectedResultRegexp, resultString)
							var matchError error
							match, matchError = regexp.MatchString(expectedResultRegexp, resultString)
							//match, matchError = regexp.MatchString("[0-9]", "7")
							if matchError != nil {
								Infof(err.Error())
							}
						} else {
							Debugf("Test %s matching against string...", testID)
							expectedResult = h.TestStringReplacements(expectedResult, r1, r2, r3)
							comparisonString = fmt.Sprintf("\nTest: %s\n\tExpected:\t%v\n\tGot:\t\t%v", testID, expectedResult, resultString)
							match = (resultString == expectedResult)
						}

						if match {
							Debugf("%s\n\tpassed! :D", comparisonString)
							passed.p("passed! ✔")
						} else {
							err = fmt.Errorf(comparisonString)
							failed.pf(fmt.Sprintf("\n=====================\n%s\n\tfailed! m(\n=====================", comparisonString))
						}
					}
				}
			}

			if err != nil {
				errs = append(errs, err)
				err = nil
			}
		}
		// restore the state for the next test file
		e := h.Reset()
		if e != nil {
			panic(e)
		}
	}
	if len(errs) == 0 {
		passed.p(fmt.Sprintf("\n==================================================================\n\t\t+++++ All tests passed :D +++++\n=================================================================="))
	} else {
		failed.pf(fmt.Sprintf("\n==================================================================\n\t\t+++++ %d test(s) failed :( +++++\n==================================================================", len(errs)))
	}
	return errs
}

// GetProperty returns the value of a DNA property
func (h *Holochain) GetProperty(prop string) (property string, err error) {
	if prop == ID_PROPERTY || prop == AGENT_ID_PROPERTY || prop == AGENT_NAME_PROPERTY {
		ChangeAppProperty.Log()
	} else {
		property = h.Properties[prop]
	}
	return
}

// Reset deletes all chain and dht data and resets data structures
func (h *Holochain) Reset() (err error) {

	h.dnaHash = Hash{}
	h.agentHash = Hash{}

	if h.chain.s != nil {
		h.chain.s.Close()
	}

	/*	err = h.store.Remove()
		if err != nil {
			panic(err)
		}
	*/
	err = os.RemoveAll(h.path + "/" + DNAHashFileName)
	if err != nil {
		panic(err)
	}

	err = os.RemoveAll(h.path + "/" + StoreFileName + ".db")
	if err != nil {
		panic(err)
	}
	err = os.RemoveAll(h.path + "/dht.db")
	if err != nil {
		panic(err)
	}
	/*
		err = h.store.Init()
		if err != nil {
			panic(err)
		}
	*/
	h.chain = NewChain()
	h.dht = NewDHT(h)
	return
}

// DHT exposes the DHT structure
func (h *Holochain) DHT() *DHT {
	return h.dht
}

// HashSpec exposes the hashSpec structure
func (h *Holochain) HashSpec() HashSpec {
	return h.hashSpec
}
