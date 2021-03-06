package whanau

import "testing"
import "runtime"
import "strconv"
import "os"
import "fmt"
import "math/rand"
import "math"
import "time"
import crand "crypto/rand"
import "crypto/rsa"
import "sync"

func port(tag string, host int) string {
	s := "/var/tmp/824-"
	s += strconv.Itoa(os.Getuid()) + "/"
	os.Mkdir(s, 0777)
	s += "sm-"
	s += strconv.Itoa(os.Getpid()) + "-"
	s += tag + "-"
	s += strconv.Itoa(host)
	return s
}

func cleanup(ws []*WhanauServer) {
	for i := 0; i < len(ws); i++ {
		if ws[i] != nil {
			ws[i].Kill()
		}
	}
}

// TODO just for testing
func testRandomWalk(server string, steps int) string {
	args := &RandomWalkArgs{}
	args.Steps = steps
	var reply RandomWalkReply
	ok := call(server, "WhanauServer.RandomWalk", args, &reply)
	if ok && (reply.Err == OK) {
		return reply.Server
	}

	return "RANDOMWALK ERR"
}

// Test getID
func testGetId(server string, layer int) KeyType {
	args := &GetIdArgs{}
	args.Layer = layer
	var reply GetIdReply
	ok := call(server, "WhanauServer.GetId", args, &reply)
	if ok && (reply.Err == OK) {
		return reply.Key
	}

	return "GETID ERR"
}

func TestLookup(t *testing.T) {
	runtime.GOMAXPROCS(8)

	const nservers = 10
	const nkeys = 50           // keys are strings from 0 to 99
	const k = nkeys / nservers // keys per node

	// run setup in parallel
	// parameters
	constant := 5
	nlayers := int(math.Log(float64(k*nservers))) + 1
	nfingers := int(math.Sqrt(k * nservers))
	w := constant * int(math.Log(float64(nservers))) // number of steps in random walks, O(log n) where n = nservers
	rd := 2 * int(math.Sqrt(k*nservers))             // number of records in the db
	rs := constant * int(math.Sqrt(k*nservers))      // number of nodes to sample to get successors
	ts := 5                                          // number of successors sampled per node

	var ws []*WhanauServer = make([]*WhanauServer, nservers)
	var kvh []string = make([]string, nservers)
	defer cleanup(ws)

	for i := 0; i < nservers; i++ {
		kvh[i] = port("basic", i)
	}

	var edgeProb float32 = 0.5

	neighbors := make([][]string, nservers)
	for i := 0; i < nservers; i++ {
		neighbors[i] = make([]string, 0)
	}

	for i := 0; i < nservers; i++ {
		for j := 0; j < i; j++ {
			if j == i {
				continue
			}

			// create edge with small probability
			prob := rand.Float32()

			if prob < edgeProb {
				neighbors[i] = append(neighbors[i], kvh[j])
				neighbors[j] = append(neighbors[j], kvh[i])
			}
		}
	}

	for k := 0; k < nservers; k++ {
		ws[k] = StartServer(kvh, k, kvh[k], neighbors[k],
			make([]string, 0), nil, false, false, false,
			nlayers, nfingers, w, rd, rs, ts)
	}

	var cka [nservers]*Clerk
	for i := 0; i < nservers; i++ {
		cka[i] = MakeClerk(kvh[i])
	}

	fmt.Printf("\033[95m%s\033[0m\n", "Test: Lookup")

	keys := make([]KeyType, 0)
	records := make(map[KeyType]ValueType)
	counter := 0
	// hard code in records for each server
	for i := 0; i < nservers; i++ {
		for j := 0; j < nkeys/nservers; j++ {
			var key KeyType = KeyType(strconv.Itoa(counter))
			keys = append(keys, key)
			counter++
			val := ValueType{}
			// randomly pick 5 servers
			for kp := 0; kp < PaxosSize; kp++ {
				val.Servers = append(val.Servers, "ws"+strconv.Itoa(rand.Intn(PaxosSize)))
			}
			records[key] = val
			ws[i].kvstore[key] = val
		}
	}
	/*
		for i := 0; i < nservers; i++ {
			fmt.Printf("ws[%d].kvstore: %s\n", i, ws[i].kvstore)
		}
	*/
	c := make(chan bool) // writes true of done
	fmt.Printf("Starting setup\n")
	start := time.Now()
	for i := 0; i < nservers; i++ {
		go func(srv int) {
			ws[srv].Setup()
			c <- true
		}(i)
	}

	// wait for all setups to finish
	for i := 0; i < nservers; i++ {
		done := <-c
		DPrintf("ws[%d] setup done: %b", i, done)
	}

	elapsed := time.Since(start)
	fmt.Printf("Finished setup, time: %s\n", elapsed)

	fmt.Printf("Check key coverage in all dbs\n")

	keyset := make(map[KeyType]bool)
	for i := 0; i < len(keys); i++ {
		keyset[keys[i]] = false
	}

	for i := 0; i < nservers; i++ {
		srv := ws[i]
		for j := 0; j < len(srv.db); j++ {
			keyset[srv.db[j].Key] = true
		}
	}

	// count number of covered keys, all the false keys in keyset
	covered_count := 0
	for _, v := range keyset {
		if v {
			covered_count++
		}
	}
	fmt.Printf("key coverage in all dbs: %f\n", float64(covered_count)/float64(len(keys)))

	fmt.Printf("Check key coverage in all successor tables\n")
	keyset = make(map[KeyType]bool)
	for i := 0; i < len(keys); i++ {
		keyset[keys[i]] = false
	}

	for i := 0; i < nservers; i++ {
		srv := ws[i]
		for j := 0; j < len(srv.succ); j++ {
			for k := 0; k < len(srv.succ[j]); k++ {
				keyset[srv.succ[j][k].Key] = true
			}
		}
	}

	// count number of covered keys, all the false keys in keyset
	covered_count = 0
	missing_keys := make([]KeyType, 0)
	for k, v := range keyset {
		if v {
			covered_count++
		} else {
			missing_keys = append(missing_keys, k)
		}
	}

	fmt.Printf("key coverage in all succ: %f\n", float64(covered_count)/float64(len(keys)))
	fmt.Printf("missing keys in succs: %s\n", missing_keys)
	// check populated ids and fingers
	/*
		var x0 KeyType = "1"
		var key KeyType = "3"
		finger, layer := ws[0].ChooseFinger(x0, key, nlayers)
		fmt.Printf("chosen finger: %s, chosen layer: %d\n", finger, layer)
	*/

	fmt.Printf("Checking Try for every key from every node\n")
	numFound := 0
	numTotal := 0
	ctr := 0
	fmt.Printf("All test keys: %s\n", keys)
	for i := 0; i < nservers; i++ {
		for j := 0; j < len(keys); j++ {
			key := KeyType(keys[j])
			ctr++
			largs := &LookupArgs{key, nil}
			lreply := &LookupReply{}
			ws[i].Lookup(largs, lreply)
			if lreply.Err != OK {
				//fmt.Printf("Did not find key: %s\n", key)
			} else {
				value := lreply.Value
				// compare string arrays...
				if len(value.Servers) != len(records[key].Servers) {
					t.Fatalf("Wrong value returned (length test): %s expected: %s", value, records[key])
				}
				for k := 0; k < len(value.Servers); k++ {
					if value.Servers[k] != records[key].Servers[k] {
						t.Fatalf("Wrong value returned for key(%s): %s expected: %s", key, value, records[key])
					}
				}
				numFound++
			}
			numTotal++
		}
	}

	fmt.Printf("numFound: %d\n", numFound)
	fmt.Printf("total keys: %d\n", nkeys)
	fmt.Printf("Percent lookups successful: %f\n", float64(numFound)/float64(numTotal))
}

func TestDataIntegrityBasic(t *testing.T) {
	runtime.GOMAXPROCS(4)

	fmt.Printf("\033[95m%s\033[0m\n", "Test: Data Integrity Functions")
	sk, err := rsa.GenerateKey(crand.Reader, 2014)

	if err != nil {
		t.Fatalf("key gen err", err)
	}

	err = sk.Validate()
	if err != nil {
		t.Fatalf("Validation failed.", err)
	}

	fmt.Println("Testing verification on true value type")
	val1 := TrueValueType{"testval", "srv1", nil, &sk.PublicKey}

	sig2, _ := SignTrueValue(val1, sk)
	val1.Sign = sig2

	if VerifyTrueValue(val1) {
		fmt.Println("true value verified!")
	} else {
		t.Fatalf("TrueValue couldn't verify")
	}

	val1.TrueValue = "changed"
	if !VerifyTrueValue(val1) {
		fmt.Println("true value modification detected!")
	} else {
		t.Fatalf("True value modification not detected")
	}

	sk1, _ := rsa.GenerateKey(crand.Reader, 2014)

	val1 = TrueValueType{"testval", "srv1", nil, &sk1.PublicKey}
	val1.Sign = sig2

	if !VerifyTrueValue(val1) {
		fmt.Println("true value pk modification detected!")
	} else {
		t.Fatalf("True value PK modification not detected")
	}

}

func TestRealGetAndPut(t *testing.T) {

	runtime.GOMAXPROCS(4)

	const nservers = 10
	const nkeys = 20           // keys are strings from 0 to 99
	const k = nkeys / nservers // keys per node

	// parameters
	constant := 5
	nlayers := constant*int(math.Log(float64(k*nservers))) + 1
	nfingers := constant * int(math.Sqrt(k*nservers))
	w := constant * int(math.Log(float64(nservers))) // number of steps in random walks, O(log n) where n = nservers
	rd := constant * int(math.Sqrt(k*nservers))      // number of records in the db
	rs := constant * int(math.Sqrt(k*nservers))      // number of nodes to sample to get successors
	ts := constant                                   // number of successors sampled per node

	//fmt.Printf("nlayers is %d, w is %d\n", nlayers, w)

	var ws []*WhanauServer = make([]*WhanauServer, nservers)
	var kvh []string = make([]string, nservers)
	defer cleanup(ws)

	for i := 0; i < nservers; i++ {
		kvh[i] = port("basic", i)
	}

	master_servers := []string{kvh[0], kvh[1], kvh[2]}

	for i := 0; i < nservers; i++ {
		neighbors := make([]string, 0)
		for j := 0; j < nservers; j++ {
			if j == i {
				continue
			}
			neighbors = append(neighbors, kvh[j])
		}

		if i < 3 {
			ws[i] = StartServer(kvh, i, kvh[i], neighbors, master_servers, master_servers, true, false, false,
				nlayers, nfingers, w, rd, rs, ts)
		} else {
			ws[i] = StartServer(kvh, i, kvh[i], neighbors, master_servers, nil, false, false, false,
				nlayers, nfingers, w, rd, rs, ts)
		}
	}

	var cka [nservers]*Clerk
	for i := 0; i < nservers; i++ {
		cka[i] = MakeClerk(kvh[i])
	}

	fmt.Printf("\033[95m%s\033[0m\n", "Test: Real Lookup")

	keys := make([]KeyType, 0)
	records := make(map[KeyType]ValueType)
	counter := 0
	// hard code in records for each server
	for i := 0; i < nservers; i++ {

		paxos_cluster := []string{kvh[i], kvh[(i+1)%nservers], kvh[(i+2)%nservers]}
		wp0 := StartWhanauPaxos(paxos_cluster, 0, "", ws[i].rpc)
		wp1 := StartWhanauPaxos(paxos_cluster, 1, "", ws[(i+1)%nservers].rpc)
		wp2 := StartWhanauPaxos(paxos_cluster, 2, "", ws[(i+2)%nservers].rpc)

		for j := 0; j < nkeys/nservers; j++ {
			//var key KeyType = testKeys[counter]
			var key KeyType = KeyType(strconv.Itoa(counter))
			keys = append(keys, key)
			counter++

			//fmt.Printf("paxos_cluster is %v\n", paxos_cluster)
			val := ValueType{paxos_cluster}
			records[key] = val
			ws[i].kvstore[key] = val

			ws[i].paxosInstances[key] = *wp0
			ws[(i+1)%nservers].paxosInstances[key] = *wp1
			ws[(i+2)%nservers].paxosInstances[key] = *wp2

			val0 := TrueValueType{"hello", wp0.myaddr, nil, &ws[i].secretKey.PublicKey}
			sig0, _ := SignTrueValue(val0, ws[i].secretKey)
			val0.Sign = sig0
			wp0.db[key] = val0

			val1 := TrueValueType{"hello", wp1.myaddr, nil, &ws[(i+1)%nservers].secretKey.PublicKey}
			sig1, _ := SignTrueValue(val1, ws[(i+1)%nservers].secretKey)
			val1.Sign = sig1
			wp1.db[key] = val1

			val2 := TrueValueType{"hello", wp2.myaddr, nil, &ws[(i+2)%nservers].secretKey.PublicKey}
			sig2, _ := SignTrueValue(val2, ws[(i+2)%nservers].secretKey)
			val2.Sign = sig2
			wp2.db[key] = val2
		}
	}

	c := make(chan bool) // writes true of done
	fmt.Printf("Starting setup\n")
	start := time.Now()
	for i := 0; i < nservers; i++ {
		go func(srv int) {
			DPrintf("running ws[%d].Setup", srv)
			ws[srv].Setup()
			c <- true
		}(i)
	}

	// wait for all setups to finish
	for i := 0; i < nservers; i++ {
		done := <-c
		DPrintf("ws[%d] setup done: %b", i, done)
	}

	elapsed := time.Since(start)
	fmt.Printf("Finished setup, time: %s\n\n", elapsed)

	// start clients

	largs := &LookupArgs{"0", nil}
	lreply := &LookupReply{}
	ws[3].Lookup(largs, lreply)
	//fmt.Printf("lreply.value is %v\n", lreply.Value.Servers)

	cl := MakeClerk(kvh[0])

	fmt.Printf("Try to do a lookup from client\n")

	value := cl.ClientGet("0")
	fmt.Printf("value is %s\n", value)

	// test single value put -- an update, NOT an insert!

	cl.ClientPut("0", "helloworld")
	value = cl.ClientGet("0")

	fmt.Printf("After put: value is %v\n", value)
}

func TestEndToEnd(t *testing.T) {
	runtime.GOMAXPROCS(4)

	const nservers = 10
	const nkeys = 30           // keys are strings from 0 to 99
	const k = nkeys / nservers // keys per node

	// parameters
	constant := 5
	nlayers := constant*int(math.Log(float64(k*nservers))) + 1
	nfingers := constant * int(math.Sqrt(k*nservers))
	w := constant * int(math.Log(float64(nservers))) // number of steps in random walks, O(log n) where n = nservers
	rd := constant * int(math.Sqrt(k*nservers))      // number of records in the db
	rs := constant * int(math.Sqrt(k*nservers))      // number of nodes to sample to get successors
	ts := constant                                   // number of successors sampled per node

	fmt.Printf("nlayers is %d, w is %d\n", nlayers, w)

	var ws []*WhanauServer = make([]*WhanauServer, nservers)
	var kvh []string = make([]string, nservers)
	defer cleanup(ws)

	for i := 0; i < nservers; i++ {
		kvh[i] = port("basic", i)
	}

	master_servers := []string{kvh[0], kvh[1], kvh[2]}

	for i := 0; i < nservers; i++ {
		neighbors := make([]string, 0)
		for j := 0; j < nservers; j++ {
			if j == i {
				continue
			}
			neighbors = append(neighbors, kvh[j])
		}

		if i < 3 {
			ws[i] = StartServer(kvh, i, kvh[i], neighbors, master_servers, master_servers, true, false, false,
				nlayers, nfingers, w, rd, rs, ts)
		} else {
			ws[i] = StartServer(kvh, i, kvh[i], neighbors, master_servers, nil, false, false, false,
				nlayers, nfingers, w, rd, rs, ts)
		}
	}

	var cka [nservers]*Clerk
	for i := 0; i < nservers; i++ {
		cka[i] = MakeClerk(kvh[i])
	}

	fmt.Printf("\033[95m%s\033[0m\n", "Test: End to End")

}

func TestPendingWrites(t *testing.T) {

	runtime.GOMAXPROCS(4)

	const nservers = 10
	const nkeys = 30           // keys are strings from 0 to 99
	const k = nkeys / nservers // keys per node

	// parameters
	constant := 5
	nlayers := constant*int(math.Log(float64(k*nservers))) + 1
	nfingers := constant * int(math.Sqrt(k*nservers))
	w := constant * int(math.Log(float64(nservers))) // number of steps in random walks, O(log n) where n = nservers
	rd := constant * int(math.Sqrt(k*nservers))      // number of records in the db
	rs := constant * int(math.Sqrt(k*nservers))      // number of nodes to sample to get successors
	ts := constant                                   // number of successors sampled per node

	fmt.Printf("nlayers is %d, w is %d\n", nlayers, w)

	var ws []*WhanauServer = make([]*WhanauServer, nservers)
	var kvh []string = make([]string, nservers)
	defer cleanup(ws)

	for i := 0; i < nservers; i++ {
		kvh[i] = port("basic", i)
	}

	master_servers := []string{kvh[0], kvh[1], kvh[2]}

	for i := 0; i < nservers; i++ {
		neighbors := make([]string, 0)
		for j := 0; j < nservers; j++ {
			if j == i {
				continue
			}
			neighbors = append(neighbors, kvh[j])
		}

		if i < 3 {
			ws[i] = StartServer(kvh, i, kvh[i], neighbors, master_servers, master_servers, true, false, false,
				nlayers, nfingers, w, rd, rs, ts)
		} else {
			ws[i] = StartServer(kvh, i, kvh[i], neighbors, master_servers, nil, false, false, false,
				nlayers, nfingers, w, rd, rs, ts)
		}
	}

	var cka [nservers]*Clerk
	for i := 0; i < nservers; i++ {
		cka[i] = MakeClerk(kvh[i])
	}

	fmt.Printf("\033[95m%s\033[0m\n", "Test: Real Lookup")

	keys := make([]KeyType, 0)
	records := make(map[KeyType]ValueType)
	counter := 0
	// hard code in records for each server
	for i := 0; i < nservers; i++ {

		paxos_cluster := []string{kvh[i], kvh[(i+1)%nservers], kvh[(i+2)%nservers]}
		wp0 := StartWhanauPaxos(paxos_cluster, 0, "", ws[i].rpc)
		wp1 := StartWhanauPaxos(paxos_cluster, 1, "", ws[(i+1)%nservers].rpc)
		wp2 := StartWhanauPaxos(paxos_cluster, 2, "", ws[(i+2)%nservers].rpc)

		for j := 0; j < nkeys/nservers; j++ {
			//var key KeyType = testKeys[counter]
			var key KeyType = KeyType(strconv.Itoa(counter))
			keys = append(keys, key)
			counter++

			//fmt.Printf("paxos_cluster is %v\n", paxos_cluster)
			val := ValueType{paxos_cluster}
			records[key] = val
			ws[i].kvstore[key] = val

			ws[i].paxosInstances[key] = *wp0
			ws[(i+1)%nservers].paxosInstances[key] = *wp1
			ws[(i+2)%nservers].paxosInstances[key] = *wp2

			val0 := TrueValueType{"hello", wp0.myaddr, nil, &ws[i].secretKey.PublicKey}
			sig0, _ := SignTrueValue(val0, ws[i].secretKey)
			val0.Sign = sig0
			wp0.db[key] = val0

			val1 := TrueValueType{"hello", wp1.myaddr, nil, &ws[(i+1)%nservers].secretKey.PublicKey}
			sig1, _ := SignTrueValue(val1, ws[(i+1)%nservers].secretKey)
			val1.Sign = sig1
			wp1.db[key] = val1

			val2 := TrueValueType{"hello", wp2.myaddr, nil, &ws[(i+2)%nservers].secretKey.PublicKey}
			sig2, _ := SignTrueValue(val2, ws[(i+2)%nservers].secretKey)
			val2.Sign = sig2
			wp2.db[key] = val2
		}
	}

	c := make(chan bool) // writes true of done
	fmt.Printf("Starting setup\n")
	start := time.Now()
	for i := 0; i < nservers; i++ {
		go func(srv int) {
			DPrintf("running ws[%d].Setup", srv)
			ws[srv].Setup()
			c <- true
		}(i)
	}

	// wait for all setups to finish
	for i := 0; i < nservers; i++ {
		done := <-c
		DPrintf("ws[%d] setup done: %b", i, done)
	}

	elapsed := time.Since(start)
	fmt.Printf("Finished setup, time: %s\n", elapsed)

	// start clients

	largs := &LookupArgs{"0", nil}
	lreply := &LookupReply{}
	ws[3].Lookup(largs, lreply)
	//fmt.Printf("lreply.value is %v\n", lreply.Value.Servers)

	cl := MakeClerk(kvh[0])

	fmt.Printf("Try to do a lookup from client\n")

	value := cl.ClientGet("0")
	fmt.Printf("value is %s\n", value)

	// test single value put -- an update, NOT an insert!

	cl.ClientPut("0", "helloworld")
	value = cl.ClientGet("0")

	fmt.Printf("After put: value is %v\n", value)

	cl.ClientPut("40", "cantbefound")

	// look in the masters' pending inserts table:

	time.Sleep(1 * time.Second)

	//fmt.Printf("pending writes for master 0: %v, %v\n", ws[0].all_pending_writes, ws[0].key_to_server)
	//fmt.Printf("pending writes for master 1: %v, %v\n", ws[1].all_pending_writes, ws[1].key_to_server)
	//fmt.Printf("pending writes for master 2: %v, %v\n", ws[2].all_pending_writes, ws[2].key_to_server)

	value = cl.ClientGet("40")
	fmt.Printf("After pending insert: value is %v\n", value)

	time.Sleep(5 * time.Second)

	fmt.Printf("Starting setup from masters\n")

	go ws[0].InitiateSetup()
	go ws[1].InitiateSetup()
	go ws[2].InitiateSetup()

	time.Sleep(30 * time.Second)

	value = cl.ClientGet("40")
	fmt.Printf("After setup: value is %v\n", value)
}

// In-class demo
func TestDemo(t *testing.T) {

	runtime.GOMAXPROCS(4)

	const nservers = 20
	const nkeys = 100          // keys are strings from 0 to 99
	const k = nkeys / nservers // keys per node

	// parameters
	constant := 5
	nlayers := constant*int(math.Log(float64(k*nservers))) + 1
	nfingers := constant * int(math.Sqrt(k*nservers))
	w := constant * int(math.Log(float64(nservers))) // number of steps in random walks, O(log n) where n = nservers
	rd := constant * int(math.Sqrt(k*nservers))      // number of records in the db
	rs := constant * int(math.Sqrt(k*nservers))      // number of nodes to sample to get successors
	ts := constant                                   // number of successors sampled per node

	//fmt.Printf("nlayers is %d, w is %d\n", nlayers, w)

	var ws []*WhanauServer = make([]*WhanauServer, nservers)
	var kvh []string = make([]string, nservers)
	defer cleanup(ws)

	for i := 0; i < nservers; i++ {
		kvh[i] = port("basic", i)
	}

	//master_servers := []string{kvh[0], kvh[1], kvh[2], kvh[3], kvh[4], kvh[5], kvh[6]}
	master_servers := []string{kvh[0], kvh[1], kvh[2]}

	newservers := make([]string, len(master_servers))
	for i, _ := range master_servers {
		// we need to actually create new servers
		// to disambiguate Paxos instances
		// so that masters don't overlap

		newservers[i] = port("masterpaxos", i)
	}

	for j, srv := range newservers {
		// This is just a dummy, only for the purpose
		// of starting the Paxos handler properly.
		// No routing should happen here!

		StartServer(newservers, j, srv, nil,
			master_servers, newservers, false, false, true, nlayers, nfingers,
			w, rd, rs, ts)
	}

	for i := 0; i < nservers; i++ {
		neighbors := make([]string, 0)
		for j := 0; j < nservers; j++ {
			if j == i {
				continue
			}
			neighbors = append(neighbors, kvh[j])
		}

		if i < 3 {
			ws[i] = StartServer(kvh, i, kvh[i], neighbors, master_servers, newservers, true, false, false,
				nlayers, nfingers, w, rd, rs, ts)
		} else {
			ws[i] = StartServer(kvh, i, kvh[i], neighbors, master_servers, nil, false, false, false,
				nlayers, nfingers, w, rd, rs, ts)
		}
	}

	var cka [nservers]*Clerk
	for i := 0; i < nservers; i++ {
		cka[i] = MakeClerk(kvh[i])
	}

	fmt.Printf("\033[95m%s\033[0m\n", "Demo")

	keys := make([]KeyType, 0)
	records := make(map[KeyType]ValueType)
	counter := 0
	// hard code in records for each server
	for i := 0; i < nservers; i++ {

		paxos_cluster := []string{kvh[i], kvh[(i+1)%nservers], kvh[(i+2)%nservers], kvh[(i+3)%nservers], kvh[(i+4)%nservers], kvh[(i+5)%nservers], kvh[(i+6)%nservers]}
		wp0 := StartWhanauPaxos(paxos_cluster, 0, "", ws[i].rpc)
		wp1 := StartWhanauPaxos(paxos_cluster, 1, "", ws[(i+1)%nservers].rpc)
		wp2 := StartWhanauPaxos(paxos_cluster, 2, "", ws[(i+2)%nservers].rpc)
		wp3 := StartWhanauPaxos(paxos_cluster, 3, "", ws[(i+3)%nservers].rpc)
		wp4 := StartWhanauPaxos(paxos_cluster, 4, "", ws[(i+4)%nservers].rpc)
		wp5 := StartWhanauPaxos(paxos_cluster, 5, "", ws[(i+5)%nservers].rpc)
		wp6 := StartWhanauPaxos(paxos_cluster, 6, "", ws[(i+6)%nservers].rpc)

		for j := 0; j < nkeys/nservers; j++ {
			//var key KeyType = testKeys[counter]
			var key KeyType = KeyType(strconv.Itoa(counter))
			keys = append(keys, key)
			counter++

			//fmt.Printf("paxos_cluster is %v\n", paxos_cluster)
			val := ValueType{paxos_cluster}
			records[key] = val
			ws[i].kvstore[key] = val

			ws[i].paxosInstances[key] = *wp0
			ws[(i+1)%nservers].paxosInstances[key] = *wp1
			ws[(i+2)%nservers].paxosInstances[key] = *wp2
			ws[(i+3)%nservers].paxosInstances[key] = *wp3
			ws[(i+4)%nservers].paxosInstances[key] = *wp4
			ws[(i+5)%nservers].paxosInstances[key] = *wp5
			ws[(i+6)%nservers].paxosInstances[key] = *wp6

			val0 := TrueValueType{"hello", wp0.myaddr, nil, &ws[i].secretKey.PublicKey}
			sig0, _ := SignTrueValue(val0, ws[i].secretKey)
			val0.Sign = sig0
			wp0.db[key] = val0

			// 			val1 := TrueValueType{"hello", wp1.myaddr, nil, &ws[(i+1)%nservers].secretKey.PublicKey}
			// 			sig1, _ := SignTrueValue(val1, ws[(i+1)%nservers].secretKey)
			// 			val1.Sign = sig1
			wp1.db[key] = val0

			// 			val2 := TrueValueType{"hello", wp2.myaddr, nil, &ws[(i+2)%nservers].secretKey.PublicKey}
			// 			sig2, _ := SignTrueValue(val2, ws[(i+2)%nservers].secretKey)
			// 			val2.Sign = sig2
			wp2.db[key] = val0

			// 			val3 := TrueValueType{"hello", wp3.myaddr, nil, &ws[(i+3)%nservers].secretKey.PublicKey}
			// 			sig3, _ := SignTrueValue(val3, ws[(i+3)%nservers].secretKey)
			// 			val3.Sign = sig3
			wp3.db[key] = val0

			// 			val4 := TrueValueType{"hello", wp4.myaddr, nil, &ws[(i+4)%nservers].secretKey.PublicKey}
			// 			sig4, _ := SignTrueValue(val4, ws[(i+4)%nservers].secretKey)
			// 			val4.Sign = sig4
			wp4.db[key] = val0

			// 			val5 := TrueValueType{"hello", wp5.myaddr, nil, &ws[(i+5)%nservers].secretKey.PublicKey}
			// 			sig5, _ := SignTrueValue(val5, ws[(i+5)%nservers].secretKey)
			// 			val5.Sign = sig5
			wp5.db[key] = val0

			// 			val6 := TrueValueType{"hello", wp6.myaddr, nil, &ws[(i+6)%nservers].secretKey.PublicKey}
			// 			sig6, _ := SignTrueValue(val6, ws[(i+6)%nservers].secretKey)
			// 			val6.Sign = sig6
			wp6.db[key] = val0

		}
	}

	c := make(chan bool) // writes true of done
	fmt.Printf("Starting setup: %d servers, %d keys\n", nservers, nkeys)
	start := time.Now()
	for i := 0; i < nservers; i++ {
		go func(srv int) {
			DPrintf("running ws[%d].Setup", srv)
			ws[srv].Setup()
			c <- true
		}(i)
	}

	// wait for all setups to finish
	for i := 0; i < nservers; i++ {
		done := <-c
		DPrintf("ws[%d] setup done: %b", i, done)
	}

	elapsed := time.Since(start)
	fmt.Printf("Finished setup, time: %s\n\n", elapsed)

	// start clients

	largs := &LookupArgs{"0", nil}
	lreply := &LookupReply{}
	ws[3].Lookup(largs, lreply)

	cl := MakeClerk(kvh[0])

	fmt.Printf("Client lookup of existing key 0...\n")

	value := cl.ClientGet("0")
	fmt.Printf("Value for key 0 is %s\n\n", value)

	// test single value put -- an update, NOT an insert!

	fmt.Printf("Client update of existing key 0 with new value helloworld...\n")
	cl.ClientPut("0", "helloworld")
	value = cl.ClientGet("0")

	fmt.Printf("After put, without re-running Setup: value for key 0 is %v\n\n", value)

	fmt.Printf("Client performing insert of new key 101 with value cantbefound...\n")
	cl.ClientPut("101", "cantbefound")

	fmt.Printf("Client performing update of new key 101 with value newvalue...\n")
	cl.ClientPut("101", "newvalue")

	// look in the masters' pending inserts table:

	time.Sleep(1 * time.Second)

	value = cl.ClientGet("101")
	fmt.Printf("After insert, before Setup run: value for key 101 is %v\n\n", value)

	time.Sleep(5 * time.Second)

	fmt.Printf("Starting setup from masters\n")

	go ws[0].InitiateSetup()
	go ws[1].InitiateSetup()
	go ws[2].InitiateSetup()

	time.Sleep(30 * time.Second)

	fmt.Printf("Setup finished\n\n")

	fmt.Printf("Client lookup of inserted key 101...\n")
	value = cl.ClientGet("101")
	fmt.Printf("After setup run: value for key 101 is %v\n\n", value)

	fmt.Printf("Three random node failures...\n")
	failed := make([]int, 3)
	for i := 0; i < 3; i++ {
		fail := rand.Intn(20) // pick an idx to fail
		for IsInList(fail, failed) {
			fail = rand.Intn(20)
		}

		failed[i] = fail
		os.Remove(kvh[fail])
	}

	fmt.Printf("Failed nodes: %v\n\n", failed)

	fmt.Printf("Client lookup of existing key 5...\n")

	value = cl.ClientGet("5")
	fmt.Printf("Value for key 5 is %s\n\n", value)
}

// Testing malicious sybils, should NOT have the same output as lookup
// Redistributing some keys to sybil nodes
func TestLookupWithSybilsMalicious(t *testing.T) {
	runtime.GOMAXPROCS(8)
	iterations := 1
	for z := 0; z < iterations; z++ {
		fmt.Println("Iteration: %d \n \n", z)
		const nservers = 20
		const nkeys = 100          // keys are strings from 0 to 99
		const k = nkeys / nservers // keys per node
		const sybilProb = 0.49
<<<<<<< HEAD
        attackEdgeProb :=  0.5 // float32(z%10)/10 + 0.1
=======
		attackEdgeProb := float32(z%10)/10 + 0.1
>>>>>>> 40adc9f4072d58facc490348589e8a0e019cf235
		// run setup in parallel
		// parameters
		constant := 5
		nlayers := int(math.Log(float64(k*nservers))) + 1
		nfingers := int(math.Sqrt(k * nservers))
		w := constant * int(math.Log(float64(nservers))) // number of steps in random walks, O(log n) where n = nservers
		rd := 2 * int(math.Sqrt(k*nservers))             // number of records in the db
		rs := constant * int(math.Sqrt(k*nservers))      // number of nodes to sample to get successors
		ts := 5                                          // number of successors sampled per node
		attackCounter := 0
		numSybilServers := 10
		sybilServerCounter := 0
<<<<<<< HEAD
        var edgeProb float32 = 1
=======
		var edgeProb float32 = 0.8
>>>>>>> 40adc9f4072d58facc490348589e8a0e019cf235

		var ws []*WhanauServer = make([]*WhanauServer, nservers)
		var kvh []string = make([]string, nservers)
		var ksvh map[int]bool = make(map[int]bool)
		defer cleanup(ws)

		for i := 0; i < nservers; i++ {
			kvh[i] = port("basic", i)
			rand.Seed(time.Now().UTC().UnixNano())
			prob := rand.Float32()
			if prob > sybilProb && sybilServerCounter < numSybilServers {
				sybilServerCounter++
				// randomly make some of the servers sybil servers
				ksvh[i] = true
			}
		}

		neighbors := make([][]string, nservers)
		for i := 0; i < nservers; i++ {
			neighbors[i] = make([]string, 0)
		}

		for i := 0; i < nservers; i++ {
			for j := 0; j < i; j++ {
				_, ok := ksvh[j]
				_, ok2 := ksvh[i]

				if ok || ok2 {
					if ok && ok2 {
						// both nodes are sybil nodes
						// create edge with 100% probability
						neighbors[i] = append(neighbors[i], kvh[j])
						neighbors[j] = append(neighbors[j], kvh[i])
					} else {
						// one node is a sybil node
						// create edge with small probability
						rand.Seed(time.Now().UTC().UnixNano())
						prob := rand.Float32()

						if prob > attackEdgeProb {
							attackCounter++
							//Sybil neighbor, print out neighbors
							neighbors[i] = append(neighbors[i], kvh[j])
							neighbors[j] = append(neighbors[j], kvh[i])
						}
					}
				} else {
					// neither is sybil, create edge with given edge probability
					rand.Seed(time.Now().UTC().UnixNano())
					prob := rand.Float32()
					if prob < edgeProb {
						neighbors[i] = append(neighbors[i], kvh[j])
						neighbors[j] = append(neighbors[j], kvh[i])
					}
				}
			}
		}

		fmt.Printf("Actual number of attack edges: %d \n", attackCounter)
		fmt.Printf("Edge probability: %d \n", edgeProb)
		fmt.Printf("Attack edge probability: %d \n", attackEdgeProb)

		for k := 0; k < nservers; k++ {
			if _, ok := ksvh[k]; ok {
				ws[k] = StartServer(kvh, k, kvh[k], neighbors[k], make([]string, 0), nil, false, true, false,
					nlayers, nfingers, w, rd, rs, ts)
			} else {
				ws[k] = StartServer(kvh, k, kvh[k], neighbors[k], make([]string, 0), nil, false, false, false,
					nlayers, nfingers, w, rd, rs, ts)
			}
		}

		var cka [nservers]*Clerk
		for i := 0; i < nservers; i++ {
			cka[i] = MakeClerk(kvh[i])
		}

		fmt.Printf("\033[95m%s\033[0m\n", "Test: Lookup With Sybils")
		for i := 0; i < len(kvh); i++ {
			if _, ok := ksvh[i]; ok {
				fmt.Printf("Address of Sybil node: %s \n", kvh[i])
			}
		}

		keys := make([]KeyType, 0)
		records := make(map[KeyType]ValueType)
		counter := 0
		// Hard code records for all servers
		for i := 0; i < nservers; i++ {
			for j := 0; j < nkeys/nservers; j++ {
				//var key KeyType = testKeys[counter]
				var key KeyType = KeyType(strconv.Itoa(counter))
				if _, ok := ksvh[i]; !ok {
					keys = append(keys, key)
				}
				counter++
				val := ValueType{}
				// randomly pick 5 servers
				for kp := 0; kp < PaxosSize; kp++ {
					val.Servers = append(val.Servers, "ws"+strconv.Itoa(rand.Intn(PaxosSize)))
				}
				records[key] = val
				ws[i].kvstore[key] = val
			}
		}

		c := make(chan bool) // writes true of done
		fmt.Printf("Starting setup\n")
		start := time.Now()
		for i := 0; i < nservers; i++ {
			go func(srv int) {
				ws[srv].Setup()
				c <- true
			}(i)
		}

		// wait for all setups to finish
		for i := 0; i < nservers; i++ {
			done := <-c
			DPrintf("ws[%d] setup done: %b", i, done)
		}

		elapsed := time.Since(start)
		fmt.Printf("Finished setup, time: %s\n", elapsed)

		fmt.Printf("Checking Try for every key from every node\n")
		numFound := 0
		numTotal := 0

		fmt.Printf("All test keys: %s\n", keys)
		for i := 0; i < nservers; i++ {
			if _, ok := ksvh[i]; !ok {
				for j := 0; j < len(keys); j++ {
					key := KeyType(keys[j])
					largs := &LookupArgs{key, nil}
					lreply := &LookupReply{}
					ws[i].Lookup(largs, lreply)
					if lreply.Err != OK {
						//fmt.Printf("Did not find key: %s\n", key)
					} else {
						value := lreply.Value
						// compare string arrays...
						if len(value.Servers) != len(records[key].Servers) {
							t.Fatalf("Wrong value returned (length test): %s expected: %s", value, records[key])
						}
						for k := 0; k < len(value.Servers); k++ {
							if value.Servers[k] != records[key].Servers[k] {
								t.Fatalf("Wrong value returned for key(%s): %s expected: %s", key, value, records[key])
							}
						}
						numFound++
					}
					numTotal++
				}
			}
		}

		fmt.Printf("numFound: %d\n", numFound)
		fmt.Printf("total keys: %d\n", numTotal)
		fmt.Printf("Percent lookups successful: %f\n", float64(numFound)/float64(numTotal))
	}
}

func TestSystolic(t *testing.T) {
	runtime.GOMAXPROCS(8)

	const nservers = 10
	const nkeys = 50           // keys are strings from 0 to 99
	const k = nkeys / nservers // keys per node

	// run setup in parallel
	// parameters
	constant := 5
	nlayers := int(math.Log(float64(k*nservers))) + 1
	nfingers := int(math.Sqrt(k * nservers))
	w := constant * int(math.Log(float64(nservers))) // number of steps in random walks, O(log n) where n = nservers
	rd := 2 * int(math.Sqrt(k*nservers))             // number of records in the db
	rs := constant * int(math.Sqrt(k*nservers))      // number of nodes to sample to get successors
	ts := 5                                          // number of successors sampled per node

	var ws []*WhanauServer = make([]*WhanauServer, nservers)
	var kvh []string = make([]string, nservers)
	defer cleanup(ws)

	for i := 0; i < nservers; i++ {
		kvh[i] = port("basic", i)
	}

	for i := 0; i < nservers; i++ {
		neighbors := make([]string, 0)
		for j := 0; j < nservers; j++ {
			if j == i {
				continue
			}
			neighbors = append(neighbors, kvh[j])
		}

		ws[i] = StartServer(kvh, i, kvh[i], neighbors, make([]string, 0),
			nil, false, false, false,
			nlayers, nfingers, w, rd, rs, ts)
	}

	var cka [nservers]*Clerk
	for i := 0; i < nservers; i++ {
		cka[i] = MakeClerk(kvh[i])
	}

	fmt.Printf("\033[95m%s\033[0m\n", "Test: Systolic mixing")

	c := make(chan bool) // writes true of done
	for i := 0; i < nservers; i++ {
		go func(srv int) {
			ws[srv].PerformSystolicMixing(100)
			c <- true
		}(i)
	}

	// wait for mixing to finish
	for i := 0; i < nservers; i++ {
		done := <-c
		DPrintf("ws[%d] mixing done: %b", i, done)
	}

}

// Testing malicious sybils end to end, should NOT have the same output as lookup
// Redistributing some keys to sybil nodes
func TestRealLookupSybil(t *testing.T) {
	runtime.GOMAXPROCS(8)
	iterations := 1
	for z := 0; z < iterations; z++ {
		fmt.Printf("Iteration: %d \n \n", z)
		const nservers = 100
		const nkeys = 500          // keys are strings from 0 to nkeys
		const k = nkeys / nservers // keys per node
		const sybilProb = 0.49
		attackEdgeProb := 0.01

		// run setup in parallel
		// parameters
		constant := 5
		nlayers := int(math.Log(float64(k*nservers))) + 1
		nfingers := int(math.Sqrt(k * nservers))
		w := constant * int(math.Log(float64(nservers))) // number of steps in random walks, O(log n) where n = nservers
		rd := 2 * int(math.Sqrt(k*nservers))             // number of records in the db
		rs := constant * int(math.Sqrt(k*nservers))      // number of nodes to sample to get successors
		ts := 5                                          // number of successors sampled per node
		numAttackEdges := 0                              //4*(int(nservers / math.Log(nservers)) + 1)
		attackCounter := 0
		numSybilServers := 50
		sybilServerCounter := 0

		fmt.Printf("Max attack edges: %d \n", numAttackEdges)

		time1 := time.Now()
		var ws []*WhanauServer = make([]*WhanauServer, nservers)
		var kvh []string = make([]string, nservers)
		var ksvh map[int]bool = make(map[int]bool)
		defer cleanup(ws)

		for i := 0; i < nservers; i++ {
			kvh[i] = port("basic", i)
			rand.Seed(time.Now().UTC().UnixNano())
			prob := rand.Float64()
			if prob > sybilProb && sybilServerCounter < numSybilServers {
				sybilServerCounter++
				// randomly make some of the servers sybil servers
				ksvh[i] = true
			}
		}

		master_servers := make([]string, 0)
		// first PaxosSize servers are master servers
		for i := 0; i < PaxosSize; i++ {
			master_servers = append(master_servers, kvh[i])
		}
		fmt.Printf("Master paxos servers are %v\n", master_servers)

		neighbors := make([][]string, nservers)
		for i := 0; i < nservers; i++ {
			neighbors[i] = make([]string, 0)
		}

		for i := 0; i < nservers; i++ {
			for j := 0; j < i; j++ {
				_, ok := ksvh[j]
				_, ok2 := ksvh[i]

				if ok || ok2 {
					if ok && ok2 {
						// both nodes are sybil nodes
						// create edge with 100% probability
						neighbors[i] = append(neighbors[i], kvh[j])
						neighbors[j] = append(neighbors[j], kvh[i])
					} else {

						// one node is a sybil node
						// create edge with small probability
						rand.Seed(time.Now().UTC().UnixNano())
						prob := rand.Float64()

						/*
							if prob > attackEdgeProb+0.455 && attackCounter < numAttackEdges {
								attackCounter++
								//Sybil neighbor, print out neighbors
								neighbors[i] = append(neighbors[i], kvh[j])
								neighbors[j] = append(neighbors[j], kvh[i])
							}
						*/
						if prob < attackEdgeProb {
							attackCounter++
							//Sybil neighbor, print out neighbors
							neighbors[i] = append(neighbors[i], kvh[j])
							neighbors[j] = append(neighbors[j], kvh[i])
						}
					}

				} else {
					// neither is sybil, create edge with 100% probability
					neighbors[i] = append(neighbors[i], kvh[j])
					neighbors[j] = append(neighbors[j], kvh[i])
				}
			}
		}

		newservers := make([]string, len(master_servers))
		for i, _ := range master_servers {
			// we need to actually create new servers
			// to disambiguate Paxos instances
			// so that masters don't overlap
			newservers[i] = port("masterpaxos", i)
		}
		for j, srv := range newservers {
			// This is just a dummy, only for the purpose
			// of starting the Paxos handler properly.
			// No routing should happen here!
			StartServer(newservers, j, srv, nil,
				master_servers, newservers, false, false, true, nlayers, nfingers,
				w, rd, rs, ts)
		}

		fmt.Printf("newservers is %v\n", newservers)
		// Start servers
		for k := 0; k < nservers; k++ {

			// if malicious
			if _, ok := ksvh[k]; ok {
				if k < PaxosSize {
					// malicious master -- doesn't do anything
					ws[k] = StartServer(kvh, k, kvh[k], neighbors[k], master_servers, newservers, true, true, false, nlayers, nfingers, w, rd, rs, ts)

				} else {
					// malicious nonmaster
					ws[k] = StartServer(kvh, k, kvh[k], neighbors[k], master_servers, nil, false, true, false, nlayers, nfingers, w, rd, rs, ts)
				}
			} else {
				// not malicious
				if k < PaxosSize {
					// non malicious master
					ws[k] = StartServer(kvh, k, kvh[k], neighbors[k], master_servers, newservers, true, false, false, nlayers, nfingers, w, rd, rs, ts)
				} else {
					// normal villager
					ws[k] = StartServer(kvh, k, kvh[k], neighbors[k], master_servers, nil, false, false, false, nlayers, nfingers, w, rd, rs, ts)
				}
			}
		}

		var cka [nservers]*Clerk
		for i := 0; i < nservers; i++ {
			cka[i] = MakeClerk(kvh[i])
		}

		fmt.Printf("\033[95m%s\033[0m\n", "Test: Real Lookup With Sybils")
		for i := 0; i < len(kvh); i++ {
			if _, ok := ksvh[i]; ok {
				fmt.Printf("Address of Sybil node: %s \n", kvh[i])
			}
		}

		keys := make([]KeyType, 0)
		nonsybilkeys := make([]KeyType, 0)
		trueRecords := make(map[KeyType]string)
		counter := 0
		// Hard code records for all servers
		for i := 0; i < nservers; i++ {
			// give keys to sybil nodes as well
			/*
				if _, present := ksvh[i]; present {
					fmt.Printf(" Sybil server %d\n", i)
				}
			*/

			for j := 0; j < nkeys/nservers; j++ {
				// make key/true value
				var key KeyType = KeyType(strconv.Itoa(counter))
				var trueval string = "val" + strconv.Itoa(counter)

				keys = append(keys, key)
				if _, present := ksvh[i]; !present {
					nonsybilkeys = append(nonsybilkeys, key)
				}
				trueRecords[key] = trueval

				val := TrueValueType{trueval, ws[i].myaddr, nil, &ws[i].secretKey.PublicKey}
				sig, _ := SignTrueValue(val, ws[i].secretKey)
				val.Sign = sig

				args := &PendingArgs{key, val, ws[i].myaddr}
				reply := &PendingReply{}

				// insert key to DHT, gets processed at setup phase
				ws[i].AddPendingRPC(args, reply)
				counter++
			}
		}

		fmt.Printf("Network creating and AddPendingrpc time: %s\n", time.Since(time1))
		// initiate setup from all masters

		start := time.Now()
		for i := 0; i < PaxosSize; i++ {
			go ws[i].InitiateSetup()
		}
		time.Sleep(120 * time.Second)

		elapsed := time.Since(start)
		fmt.Printf("Finished setup from initiate setup, time: %s\n", elapsed)
		for i := 0; i < nservers; i++ {
			fmt.Printf("ws[%d].kvstore length: %s\n", i, len(ws[i].kvstore))

			for key, val := range ws[i].kvstore {
				fmt.Printf("Paxos cluster for key %s: %s\n", key, val)
			}
		}

		fmt.Printf("Finished setup, time: %s\n", elapsed)

		/*
			fmt.Printf("Check key coverage in all dbs\n")

			keyset := make(map[KeyType]bool)
			for i := 0; i < len(keys); i++ {
				keyset[keys[i]] = false
			}

			for i := 0; i < nservers; i++ {
				srv := ws[i]
				for j := 0; j < len(srv.db); j++ {
					keyset[srv.db[j].Key] = true
				}
			}

			// count number of covered keys, all the false keys in keyset
			covered_count := 0
			for _, v := range keyset {
				if v {
					covered_count++
				}
			}
			fmt.Printf("key coverage in all dbs: %f\n", float64(covered_count)/float64(len(keys)))

			fmt.Printf("Check key coverage in all successor tables\n")
			keyset = make(map[KeyType]bool)
			for i := 0; i < len(keys); i++ {
				keyset[keys[i]] = false
			}

			for i := 0; i < nservers; i++ {
				srv := ws[i]
				for j := 0; j < len(srv.succ); j++ {
					for k := 0; k < len(srv.succ[j]); k++ {
						keyset[srv.succ[j][k].Key] = true
					}
				}
			}

			// count number of covered keys, all the false keys in keyset
			covered_count = 0
			missing_keys := make([]KeyType, 0)
			for k, v := range keyset {
				if v {
					covered_count++
				} else {
					missing_keys = append(missing_keys, k)
				}
			}

			fmt.Printf("key coverage in all succ: %f\n", float64(covered_count)/float64(len(keys)))
			fmt.Printf("missing keys in succs: %s\n", missing_keys)
		*/

		fmt.Printf("Perform client lookup from all honest nodes\n")
		numFound := 0
		numTotal := 0

		chfound := make(chan int)
		for i := 0; i < nservers; i++ {
			// skip sybil node
			if _, present := ksvh[i]; present {
				fmt.Printf("skipping sybil node %s\n", cka[i])
				continue
			}

			client := cka[i]
			// only lookup key space in nonsybil nodes
			go func(client *Clerk, keys []KeyType, trueRecords map[KeyType]string) {
				fmt.Printf("Looking up all keys from client %s\n", client)
				myNumFound := 0
				for j := 0; j < len(keys); j++ {
					key := keys[j]
					val := client.ClientGet(key)
					if val == trueRecords[key] {
						//fmt.Printf("%s found key %s!\n", client, key)
						myNumFound++
					} else {
						if val != ErrNoKey && val != trueRecords[key] {
							t.Fatalf("Wrong true value for key %s, returned %s expected: %s\n", key, val, trueRecords[key])
						}
						fmt.Printf("Key %s not found D: \n", key)
					}
				}

				fmt.Printf("total found by client %s, %d\n", client, myNumFound)
				chfound <- myNumFound
			}(client, nonsybilkeys, trueRecords)

			numTotal += len(nonsybilkeys)
		}

		var mutex sync.Mutex
		for i := 0; i < nservers-len(ksvh); i++ {
			tmp := <-chfound
			fmt.Printf("Received myNumFound: %d\n", tmp)

			mutex.Lock()
			numFound += tmp
			mutex.Unlock()
		}

		fmt.Printf("nservers: %d, nkeys: %d, sybilProb: %v, numSybilServers: %v\n", nservers, nkeys, sybilProb, len(ksvh))
		fmt.Printf("Actual number of attack edges: %d\n", attackCounter)
		fmt.Printf("numFound: %d\n", numFound)
		fmt.Printf("total keys: %d\n", numTotal)
		fmt.Printf("Percent True lookups successful: %f\n", float64(numFound)/float64(numTotal))

	}
}

// Testing paxos cluster composition
func TestClusterComp(t *testing.T) {
	runtime.GOMAXPROCS(8)
	iterations := 3
	for z := 0; z < iterations; z++ {
		fmt.Printf("Iteration: %d \n \n", z)
		const nservers = 100
		const nkeys = 500          // keys are strings from 0 to nkeys
		const k = nkeys / nservers // keys per node
		const sybilProb = 0.49
		attackEdgeProb := 0.1

		// run setup in parallel
		// parameters
		constant := 5
		nlayers := int(math.Log(float64(k*nservers))) + 1
		nfingers := int(math.Sqrt(k * nservers))
		w := constant * int(math.Log(float64(nservers))) // number of steps in random walks, O(log n) where n = nservers
		rd := 2 * int(math.Sqrt(k*nservers))             // number of records in the db
		rs := constant * int(math.Sqrt(k*nservers))      // number of nodes to sample to get successors
		ts := 5                                          // number of successors sampled per node
		numAttackEdges := 0                              //4*(int(nservers / math.Log(nservers)) + 1)
		attackCounter := 0
		numSybilServers := 50
		sybilServerCounter := 0

		fmt.Printf("Max attack edges: %d \n", numAttackEdges)

		time1 := time.Now()
		var ws []*WhanauServer = make([]*WhanauServer, nservers)
		var kvh []string = make([]string, nservers)
		var ksvh map[int]bool = make(map[int]bool)
		defer cleanup(ws)

		for i := 0; i < nservers; i++ {
			kvh[i] = port("basic", i)
			rand.Seed(time.Now().UTC().UnixNano())
			prob := rand.Float64()
			if prob > sybilProb && sybilServerCounter < numSybilServers {
				sybilServerCounter++
				// randomly make some of the servers sybil servers
				ksvh[i] = true
			}
		}

		master_servers := make([]string, 0)
		// first PaxosSize servers are master servers
		for i := 0; i < PaxosSize; i++ {
			master_servers = append(master_servers, kvh[i])
		}
		fmt.Printf("Master paxos servers are %v\n", master_servers)

		neighbors := make([][]string, nservers)
		for i := 0; i < nservers; i++ {
			neighbors[i] = make([]string, 0)
		}

		for i := 0; i < nservers; i++ {
			for j := 0; j < i; j++ {
				_, ok := ksvh[j]
				_, ok2 := ksvh[i]

				if ok || ok2 {
					if ok && ok2 {
						// both nodes are sybil nodes
						// create edge with 100% probability
						neighbors[i] = append(neighbors[i], kvh[j])
						neighbors[j] = append(neighbors[j], kvh[i])
					} else {

						// one node is a sybil node
						// create edge with small probability
						rand.Seed(time.Now().UTC().UnixNano())
						prob := rand.Float64()

						if prob < attackEdgeProb {
							attackCounter++
							//Sybil neighbor, print out neighbors
							neighbors[i] = append(neighbors[i], kvh[j])
							neighbors[j] = append(neighbors[j], kvh[i])
						}
					}

				} else {
					// neither is sybil, create edge with 100% probability
					neighbors[i] = append(neighbors[i], kvh[j])
					neighbors[j] = append(neighbors[j], kvh[i])
				}
			}
		}

		newservers := make([]string, len(master_servers))
		for i, _ := range master_servers {
			// we need to actually create new servers
			// to disambiguate Paxos instances
			// so that masters don't overlap
			newservers[i] = port("masterpaxos", i)
		}
		for j, srv := range newservers {
			// This is just a dummy, only for the purpose
			// of starting the Paxos handler properly.
			// No routing should happen here!
			StartServer(newservers, j, srv, nil,
				master_servers, newservers, false, false, true, nlayers, nfingers,
				w, rd, rs, ts)
		}

		fmt.Printf("newservers is %v\n", newservers)
		// Start servers
		for k := 0; k < nservers; k++ {

			// if malicious
			if _, ok := ksvh[k]; ok {
				if k < PaxosSize {
					// malicious master -- doesn't do anything
					ws[k] = StartServer(kvh, k, kvh[k], neighbors[k], master_servers, newservers, true, true, false, nlayers, nfingers, w, rd, rs, ts)

				} else {
					// malicious nonmaster
					ws[k] = StartServer(kvh, k, kvh[k], neighbors[k], master_servers, nil, false, true, false, nlayers, nfingers, w, rd, rs, ts)
				}
			} else {
				// not malicious
				if k < PaxosSize {
					// non malicious master
					ws[k] = StartServer(kvh, k, kvh[k], neighbors[k], master_servers, newservers, true, false, false, nlayers, nfingers, w, rd, rs, ts)
				} else {
					// normal villager
					ws[k] = StartServer(kvh, k, kvh[k], neighbors[k], master_servers, nil, false, false, false, nlayers, nfingers, w, rd, rs, ts)
				}
			}
		}

		var cka [nservers]*Clerk
		for i := 0; i < nservers; i++ {
			cka[i] = MakeClerk(kvh[i])
		}

		fmt.Printf("\033[95m%s\033[0m\n", "Test: Paxos Cluster Composition")
		sybiladdrs := make(map[string]bool)
		for i := 0; i < len(kvh); i++ {
			if _, ok := ksvh[i]; ok {
				fmt.Printf("Address of Sybil node: %s \n", kvh[i])
				sybiladdrs[kvh[i]] = true
			}
		}

		keys := make([]KeyType, 0)
		nonsybilkeys := make([]KeyType, 0)
		trueRecords := make(map[KeyType]string)
		counter := 0
		// Hard code records for all servers
		for i := 0; i < nservers; i++ {
			// give keys to sybil nodes as well
			/*
				if _, present := ksvh[i]; present {
					fmt.Printf(" Sybil server %d\n", i)
				}
			*/

			for j := 0; j < nkeys/nservers; j++ {
				// make key/true value
				var key KeyType = KeyType(strconv.Itoa(counter))
				var trueval string = "val" + strconv.Itoa(counter)

				keys = append(keys, key)
				if _, present := ksvh[i]; !present {
					nonsybilkeys = append(nonsybilkeys, key)
				}
				trueRecords[key] = trueval

				val := TrueValueType{trueval, ws[i].myaddr, nil, &ws[i].secretKey.PublicKey}
				sig, _ := SignTrueValue(val, ws[i].secretKey)
				val.Sign = sig

				args := &PendingArgs{key, val, ws[i].myaddr}
				reply := &PendingReply{}

				// insert key to DHT, gets processed at setup phase
				ws[i].AddPendingRPC(args, reply)
				counter++
			}
		}

		fmt.Printf("Network creating and AddPendingrpc time: %s\n", time.Since(time1))
		// initiate setup from all masters

		start := time.Now()
		for i := 0; i < PaxosSize; i++ {
			go ws[i].InitiateSetup()
		}
		time.Sleep(120 * time.Second)

		elapsed := time.Since(start)
		fmt.Printf("Finished setup from initiate setup, time: %s\n", elapsed)
		for i := 0; i < nservers; i++ {
			fmt.Printf("ws[%d].kvstore length: %s\n", i, len(ws[i].kvstore))

			for key, val := range ws[i].kvstore {
				fmt.Printf("Paxos cluster for key %s: %s\n", key, val)
			}
		}

		fmt.Printf("Finished setup, time: %s\n", elapsed)

		fmt.Printf("Count paxos cluster composition\n")

		totalClusters := 0
		numMajority := 0
		for i := 0; i < nservers; i++ {
			// skip sybil node
			if _, present := ksvh[i]; present {
				//fmt.Printf("skipping sybil node %s\n", cka[i])
				continue
			}

			for key, cluster := range ws[i].kvstore {
				//fmt.Printf("Cluster for key %s is %s\n", key, cluster)
				numSybil := 0

				for s := 0; s < len(cluster.Servers); s++ {
					srv := cluster.Servers[s]
					if _, present := sybiladdrs[srv]; present {
						numSybil++
					}
				}
				if (float64(numSybil) / float64(len(cluster.Servers))) > 0.5 {
					fmt.Printf("Majority found for key %s numSybil: %d, cluster size %d\n", key, numSybil, len(cluster.Servers))
					numMajority++
				}
				totalClusters++
			}

		}

    fmt.Printf("nservers: %d, nkeys: %d, sybilProb: %v, numSybilServers: %v, attackEdgeProb: %v\n", nservers, nkeys, sybilProb, len(ksvh), attackEdgeProb)
		fmt.Printf("Actual number of attack edges: %d\n", attackCounter)
    fmt.Printf("Cluster size: %d\n", PaxosSize)
		fmt.Printf("totalClusters: %d\n", totalClusters)
		fmt.Printf("numMajority: %d\n", numMajority)
		fmt.Printf("Percent clusters with sybil majoriy: %v\n", float64(numMajority)/float64(totalClusters))
	}
}
