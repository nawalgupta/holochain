package holochain

import (
	"fmt"
	zygo "github.com/glycerine/zygomys/repl"
	peer "github.com/libp2p/go-libp2p-peer"
	. "github.com/smartystreets/goconvey/convey"
	"testing"
	"time"
)

func TestNewZygoNucleus(t *testing.T) {
	Convey("new should create a nucleus", t, func() {
		v, err := NewZygoNucleus(nil, `(+ 1 1)`)
		z := v.(*ZygoNucleus)
		So(err, ShouldBeNil)
		So(z.lastResult.(*zygo.SexpInt).Val, ShouldEqual, 2)
	})
	Convey("new fail to create nucleus when code is bad", t, func() {
		v, err := NewZygoNucleus(nil, "(should make a zygo syntax error")
		So(v, ShouldBeNil)
		So(err.Error(), ShouldEqual, "Zygomys load error: Error on line 1: parser needs more input\n")
	})

	Convey("it should have an App structure:", t, func() {
		d, _, h := prepareTestChain("test")
		defer cleanupTestDir(d)

		v, err := NewZygoNucleus(h, "")
		So(err, ShouldBeNil)
		z := v.(*ZygoNucleus)

		_, err = z.Run("App_DNAHash")
		So(err, ShouldBeNil)
		s := z.lastResult.(*zygo.SexpStr).S
		So(s, ShouldEqual, h.dnaHash.String())
		_, err = z.Run("App_AgentHash")
		So(err, ShouldBeNil)
		s = z.lastResult.(*zygo.SexpStr).S
		So(s, ShouldEqual, h.agentHash.String())

		_, err = z.Run("App_AgentStr")
		So(err, ShouldBeNil)
		s = z.lastResult.(*zygo.SexpStr).S
		So(s, ShouldEqual, h.Agent().Name())

		_, err = z.Run("App_KeyHash")
		So(err, ShouldBeNil)
		s = z.lastResult.(*zygo.SexpStr).S
		So(s, ShouldEqual, peer.IDB58Encode(h.id))
	})

	Convey("should have the built in functions:", t, func() {
		d, _, h := prepareTestChain("test")
		defer cleanupTestDir(d)

		v, err := NewZygoNucleus(h, "")
		So(err, ShouldBeNil)
		z := v.(*ZygoNucleus)
		Convey("version", func() {
			_, err = z.Run("(version)")
			So(err, ShouldBeNil)
			So(z.lastResult.(*zygo.SexpStr).S, ShouldEqual, VersionStr)
		})

		Convey("atoi", func() {
			_, err = z.Run(`(atoi "3141")`)
			So(err, ShouldBeNil)
			So(z.lastResult.(*zygo.SexpInt).Val, ShouldEqual, 3141)
			_, err = z.Run(`(atoi 1)`)
			So(err.Error(), ShouldEqual, "Zygomys exec error: Error calling 'atoi': argument to atoi should be string")
		})
		Convey("isprime", func() {
			_, err = z.Run(`(isprime 100)`)
			So(err, ShouldBeNil)
			So(z.lastResult.(*zygo.SexpBool).Val, ShouldEqual, false)
			_, err = z.Run(`(isprime 7)`)
			So(err, ShouldBeNil)
			So(z.lastResult.(*zygo.SexpBool).Val, ShouldEqual, true)
			_, err = z.Run(`(isprime "fish")`)
			So(err.Error(), ShouldEqual, "Zygomys exec error: Error calling 'isprime': argument to isprime should be int")
		})
		Convey("property", func() {
			_, err = z.Run(`(property "description")`)
			So(err, ShouldBeNil)
			So(z.lastResult.(*zygo.SexpStr).S, ShouldEqual, "a bogus test holochain")

			ShouldLog(&infoLog, "Warning: Getting special properties via property() is deprecated as of 3. Returning nil values.  Use App* instead\n", func() {
				_, err = z.Run(`(property "` + ID_PROPERTY + `")`)
				So(err, ShouldBeNil)
			})

		})
	})
}

func TestZygoRequires(t *testing.T) {
	Convey("it should log a warning if requires not defined", t, func() {
		z, _ := NewZygoNucleus(nil, ``)
		var err error
		ShouldLog(&infoLog, "Warning: Zomes must define 'requires' function as of version 2, assuming no requirements.\n", func() {
			err = z.ChainRequires()
			So(err, ShouldBeNil)
		})
	})

	Convey("it should fail if the requires function returns non object", t, func() {
		z, _ := NewZygoNucleus(nil, `(defn requires [] false)`)
		err := z.ChainRequires()
		So(err.Error(), ShouldEqual, "require should return a hash")
	})

	Convey("it should fail if the requires function returns version greater than current", t, func() {
		nextVersion := fmt.Sprintf("%d", Version+1)
		z, _ := NewZygoNucleus(nil, `(defn requires [] (hash version:`+nextVersion+`))`)
		err := z.ChainRequires()
		So(err.Error(), ShouldEqual, "Zome requires version "+nextVersion)
	})
}

func TestZygoGenesis(t *testing.T) {
	Convey("it should fail if the genesis function returns false", t, func() {
		z, _ := NewZygoNucleus(nil, `(defn genesis [] false)`)
		err := z.ChainGenesis()
		So(err.Error(), ShouldEqual, "genesis failed")
	})
	Convey("it should work if the genesis function returns true", t, func() {
		z, _ := NewZygoNucleus(nil, `(defn genesis [] true)`)
		err := z.ChainGenesis()
		So(err, ShouldBeNil)
	})
}

func TestZygoValidateEntry(t *testing.T) {
	p := ValidationProps{}
	Convey("should run an entry value against the defined validator for string data", t, func() {
		v, err := NewZygoNucleus(nil, `(defn validate [name entry meta] (cond (== entry "fish") true false))`)
		d := EntryDef{Name: "myData", DataFormat: DataFormatString}
		err = v.ValidateEntry(&d, &GobEntry{C: "cow"}, &p)
		So(err.Error(), ShouldEqual, "Invalid entry: cow")
		err = v.ValidateEntry(&d, &GobEntry{C: "fish"}, &p)
		So(err, ShouldBeNil)
	})
	Convey("should run an entry value against the defined validator for zygo data", t, func() {
		v, err := NewZygoNucleus(nil, `(defn validate [name entry meta] (cond (== entry "fish") true false))`)
		d := EntryDef{Name: "myData", DataFormat: DataFormatRawZygo}
		err = v.ValidateEntry(&d, &GobEntry{C: "\"cow\""}, &p)
		So(err.Error(), ShouldEqual, "Invalid entry: \"cow\"")
		err = v.ValidateEntry(&d, &GobEntry{C: "\"fish\""}, &p)
		So(err, ShouldBeNil)
	})
	Convey("should run an entry value against the defined validator for json data", t, func() {
		v, err := NewZygoNucleus(nil, `(defn validate [name entry meta] (cond (== (hget entry data:) "fish") true false))`)
		d := EntryDef{Name: "myData", DataFormat: DataFormatJSON}
		err = v.ValidateEntry(&d, &GobEntry{C: `{"data":"cow"}`}, &p)
		So(err.Error(), ShouldEqual, `Invalid entry: {"data":"cow"}`)
		err = v.ValidateEntry(&d, &GobEntry{C: `{"data":"fish"}`}, &p)
		So(err, ShouldBeNil)
	})
}

func TestZygoExposeCall(t *testing.T) {
	var z *ZygoNucleus
	Convey("should run", t, func() {
		v, err := NewZygoNucleus(nil, `
(expose "cater" STRING)
(defn cater [x] (concat "result: " x))
(expose "adder" STRING)
(defn adder [x] (+ (atoi x) 2))
(expose "jtest" JSON)
(defn jtest [x] (begin (hset x output: (* (-> x input:) 2)) x))
(expose "emptyParametersJson" JSON)
(defn emptyParametersJson [x] (unjson (raw "[{\"a\":\"b\"}]")))
`)

		So(err, ShouldBeNil)
		z = v.(*ZygoNucleus)
		_, err = z.env.Run()
		So(err, ShouldBeNil)
	})

	Convey("should build up interfaces list", t, func() {
		i := z.Interfaces()
		So(fmt.Sprintf("%v", i), ShouldEqual, "[{cater 0} {adder 0} {jtest 1} {emptyParametersJson 1}]")
	})
	Convey("should allow calling exposed STRING based functions", t, func() {
		result, err := z.Call("cater", "fish \"zippy\"")
		So(err, ShouldBeNil)
		So(result.(string), ShouldEqual, "result: fish \"zippy\"")

		result, err = z.Call("adder", "10")
		So(err, ShouldBeNil)
		So(result.(string), ShouldEqual, "12")
	})
	Convey("should allow calling exposed JSON based functions", t, func() {
		result, err := z.Call("jtest", `{"input": 2}`)
		So(err, ShouldBeNil)
		So(string(result.([]byte)), ShouldEqual, `{"Atype":"hash", "input":2, "output":4, "zKeyOrder":["input", "output"]}`)
	})
	Convey("should allow a function declared with JSON parameter to be called with no parameter", t, func() {
		result, err := z.Call("emptyParametersJson", "")
		So(err, ShouldBeNil)
		So(string(result.([]byte)), ShouldEqual, "[{\"Atype\":\"hash\", \"a\":\"b\", \"zKeyOrder\":[\"a\"]}]")
	})
}

func TestZygoDHT(t *testing.T) {
	d, _, h := prepareTestChain("test")
	defer cleanupTestDir(d)

	data := "2"

	// add an entry onto the chain
	now := time.Unix(1, 1) // pick a constant time so the test will always work
	e := GobEntry{C: data}
	_, hd, err := h.NewEntry(now, "myData", &e)
	if err != nil {
		panic(err)
	}

	hash := hd.EntryLink
	Convey("it should have a put function", t, func() {
		v, err := NewZygoNucleus(h, fmt.Sprintf(`(put "%s")`, hash.String()))
		So(err, ShouldBeNil)
		z := v.(*ZygoNucleus)
		r, err := z.lastResult.(*zygo.SexpHash).HashGet(z.env, z.env.MakeSymbol("result"))
		So(err, ShouldBeNil)
		So(r.(*zygo.SexpStr).S, ShouldEqual, "ok")
	})

	Convey("it should have a get function", t, func() {
		v, err := NewZygoNucleus(h, fmt.Sprintf(`(get "%s")`, hash.String()))
		So(err, ShouldBeNil)
		z := v.(*ZygoNucleus)
		r, err := z.lastResult.(*zygo.SexpHash).HashGet(z.env, z.env.MakeSymbol("error"))
		So(err, ShouldBeNil)
		So(r.(*zygo.SexpStr).S, ShouldEqual, "hash not found")

		if err := h.dht.simHandlePutReqs(); err != nil {
			panic(err)
		}
		v, err = NewZygoNucleus(h, fmt.Sprintf(`(get "%s")`, hash.String()))
		So(err, ShouldBeNil)
		z = v.(*ZygoNucleus)
		r, err = z.lastResult.(*zygo.SexpHash).HashGet(z.env, z.env.MakeSymbol("result"))
		So(err, ShouldBeNil)
		So(r.(*zygo.SexpStr).S, ShouldEqual, `"2"`)
	})

	e = GobEntry{C: `{"firstName":"Zippy","lastName":"Pinhead"}`}
	_, mhd, _ := h.NewEntry(now, "profile", &e)
	metaHash := mhd.EntryLink
	//b, _ := e.Marshal()

	Convey("it should have a putmeta function", t, func() {
		v, err := NewZygoNucleus(h, fmt.Sprintf(`(putmeta "%s" "%s" "myMetaTag")`, hash.String(), metaHash.String()))
		So(err, ShouldBeNil)
		z := v.(*ZygoNucleus)

		sh := z.lastResult.(*zygo.SexpHash)
		r, err := sh.HashGet(z.env, z.env.MakeSymbol("result"))
		So(err, ShouldBeNil)
		So(r.(*zygo.SexpStr).S, ShouldEqual, "ok")
	})

	if err := h.dht.simHandlePutReqs(); err != nil {
		panic(err)
	}

	Convey("it should have a getmeta function", t, func() {
		v, err := NewZygoNucleus(h, fmt.Sprintf(`(getmeta "%s" "myMetaTag")`, hash.String()))
		So(err, ShouldBeNil)
		z := v.(*ZygoNucleus)
		sh := z.lastResult.(*zygo.SexpHash)

		r, err := sh.HashGet(z.env, z.env.MakeSymbol("result"))
		So(err, ShouldBeNil)
		So(r.(*zygo.SexpStr).S, ShouldEqual, `[{"E":{"C":"{\"firstName\":\"Zippy\",\"lastName\":\"Pinhead\"}"},"H":"QmYeinX5vhuA91D3v24YbgyLofw9QAxY6PoATrBHnRwbtt"}]`)
	})
}
