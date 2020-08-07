package client

import (
	"fmt"
	"strings"

	log "github.com/golang/glog"
)

// virtual db is to Handle
// <1> different set of redis db data aggreggation
// <2> or non default TARGET_DEFINED stream subscription

// For virtual db path
const (
	DbIdx    uint = iota // DB name is the first element (no. 0) in path slice.
	TblIdx               // Table name is the second element (no. 1) in path slice.
	KeyIdx               // Key name is the first element (no. 2) in path slice.
	FieldIdx             // Field name is the first element (no. 3) in path slice.
)

type v2rTranslate func([]string) ([]tablePath, error)

type pathTransFunc struct {
	path      []string
	transFunc v2rTranslate
}

var (
	v2rTrie *Trie

	// Port name to oid map in COUNTERS table of COUNTERS_DB
	countersPortNameMap = make(map[string]string)

	// Queue name to oid map in COUNTERS table of COUNTERS_DB
	countersQueueNameMap = make(map[string]string)

	// Alias translation: from vendor port name to sonic interface name
	alias2nameMap = make(map[string]string)
	// Alias translation: from sonic interface name to vendor port name
	name2aliasMap = make(map[string]string)

	// SONiC interface name to their PFC-WD enabled queues, then to oid map
	countersPfcwdNameMap = make(map[string]map[string]string)

	// path2TFuncTbl is used to populate trie tree which is reponsible
	// for virtual path to real data path translation
	pathTransFuncTbl = []pathTransFunc{
		{ // stats for one or all Ethernet ports
			path:      []string{"COUNTERS_DB", "COUNTERS", "Ethernet*"},
			transFunc: v2rTranslate(v2rEthPortStats),
		}, { // specific field stats for one or all Ethernet ports
			path:      []string{"COUNTERS_DB", "COUNTERS", "Ethernet*", "*"},
			transFunc: v2rTranslate(v2rEthPortFieldStats),
		}, { // Queue stats for one or all Ethernet ports
			path:      []string{"COUNTERS_DB", "COUNTERS", "Ethernet*", "Queues"},
			transFunc: v2rTranslate(v2rEthPortQueStats),
		}, { // PFC WD stats for one or all Ethernet ports
			path:      []string{"COUNTERS_DB", "COUNTERS", "Ethernet*", "Pfcwd"},
			transFunc: v2rTranslate(v2rEthPortPfcwdStats),
		},
	}
)

func (t *Trie) v2rTriePopulate() {
	for _, pt := range pathTransFuncTbl {
		n := t.Add(pt.path, pt.transFunc)
		if n.meta.(v2rTranslate) == nil {
			log.V(1).Infof("Failed to add trie node for %v with %v", pt.path, pt.transFunc)
		} else {
			log.V(2).Infof("Add trie node for %v with %v", pt.path, pt.transFunc)
		}

	}
}

func initCountersQueueNameMap() error {
	var err error
	if len(countersQueueNameMap) == 0 {
		countersQueueNameMap, err = getCountersMap("COUNTERS_QUEUE_NAME_MAP")
		if err != nil {
			return err
		}
	}
	return nil
}

func initCountersPortNameMap() error {
	var err error
	if len(countersPortNameMap) == 0 {
		countersPortNameMap, err = getCountersMap("COUNTERS_PORT_NAME_MAP")
		if err != nil {
			return err
		}
	}
	return nil
}

func initAliasMap() error {
	var err error
	if len(alias2nameMap) == 0 {
		alias2nameMap, name2aliasMap, err = getAliasMap()
		if err != nil {
			return err
		}
	}
	return nil
}

func initCountersPfcwdNameMap() error {
	var err error
	if len(countersPfcwdNameMap) == 0 {
		countersPfcwdNameMap, err = getPfcwdMap()
		if err != nil {
			return err
		}
	}
	return nil
}

// Get the mapping between sonic interface name and oids of their PFC-WD enabled queues in COUNTERS_DB
func getPfcwdMap() (map[string]map[string]string, error) {
	var pfcwdName_map = make(map[string]map[string]string)

	dbName := "CONFIG_DB"
	separator, _ := GetTableKeySeparator(dbName)
	redisDb, _ := Target2RedisDb[dbName]
	_, err := redisDb.Ping().Result()
	if err != nil {
		log.V(1).Infof("Can not connect to %v, err: %v", dbName, err)
		return nil, err
	}

	keyName := fmt.Sprintf("PFC_WD%v*", separator)
	log.V(7).Infof("Get Pfcwd Key Name %v", keyName)
	resp, err := redisDb.Keys(keyName).Result()
	log.V(7).Infof("Database response %v", resp)
	if err != nil {
		log.V(1).Infof("redis get keys failed for %v, key = %v, err: %v", dbName, keyName, err)
		return nil, err
	}

	if len(resp) == 0 {
		// PFC WD service not enabled on device
		log.V(1).Infof("PFC WD not enabled on device")
		return nil, nil
	}

	for _, key := range resp {
		if len(key) > 15 && strings.EqualFold(key[:15], "PFC_WD|Ethernet") { //Need to be long enough so that we know it is a port not PFC_WD|Global and so we don't go beyond the end of the string.
			name := key[7:] //Should be 7, but is there a more resilient way to do this?
			pfcwdName_map[name] = make(map[string]string)
			log.V(7).Infof("key 8: %v ,key: %v name: %v , pfcwdName_map: %v", key[8:8], key, name, pfcwdName_map[name])
		}
	}
	// Get Queue indexes that are enabled with PFC-WD
	keyName = "PORT_QOS_MAP*"
	resp, err = redisDb.Keys(keyName).Result()
	if err != nil {
		log.V(1).Infof("redis get keys failed for %v, key = %v, err: %v", dbName, keyName, err)
		return nil, err
	}
	if len(resp) == 0 {
		log.V(1).Infof("PFC WD not enabled on device")
		return nil, nil
	}
	qos_key := resp[0]
	log.V(7).Infof("Line 165 %v", resp)

	fieldName := "pfc_enable"
	priorities, err := redisDb.HGet(qos_key, fieldName).Result()
	if err != nil {
		log.V(1).Infof("redis get field failed for %v, key = %v, field = %v, err: %v", dbName, qos_key, fieldName, err)
		return nil, err
	}

	keyName = fmt.Sprintf("MAP_PFC_PRIORITY_TO_QUEUE%vAZURE", separator)
	pfc_queue_map, err := redisDb.HGetAll(keyName).Result()
	if err != nil {
		log.V(1).Infof("redis get fields failed for %v, key = %v, err: %v", dbName, keyName, err)
		return nil, err
	}

	var indices []string
	for _, p := range strings.Split(priorities, ",") {
		_, ok := pfc_queue_map[p]
		if !ok {
			log.V(1).Infof("Missing mapping between PFC priority %v to queue", p)
		} else {
			indices = append(indices, pfc_queue_map[p])
		}
	}

	if len(countersQueueNameMap) == 0 {
		log.V(1).Infof("COUNTERS_QUEUE_NAME_MAP is empty")
		return nil, nil
	}

	log.V(7).Infof("COUNTERS_QUEUE_NAME_MAP: %v", countersQueueNameMap)
	log.V(7).Infof("Ports: %v", pfcwdName_map)

	var queue_key string
	queue_separator, _ := GetTableKeySeparator("COUNTERS_DB")
	for port, _ := range pfcwdName_map {
		for _, indice := range indices {
			queue_key = port + queue_separator + indice
			oid, ok := countersQueueNameMap[queue_key]
			if !ok {
				return nil, fmt.Errorf("key %v not exists in COUNTERS_QUEUE_NAME_MAP", queue_key)
			}
			pfcwdName_map[port][queue_key] = oid
		}
	}

	log.V(6).Infof("countersPfcwdNameMap: %v", pfcwdName_map)
	return pfcwdName_map, nil
}

// Get the mapping between sonic interface name and vendor alias
func getAliasMap() (map[string]string, map[string]string, error) {
	var alias2name_map = make(map[string]string)
	var name2alias_map = make(map[string]string)

	dbName := "CONFIG_DB"
	separator, _ := GetTableKeySeparator(dbName)
	redisDb, _ := Target2RedisDb[dbName]
	_, err := redisDb.Ping().Result()
	if err != nil {
		log.V(1).Infof("Can not connect to %v, err: %v", dbName, err)
		return nil, nil, err
	}

	keyName := fmt.Sprintf("PORT%v*", separator)
	resp, err := redisDb.Keys(keyName).Result()
	if err != nil {
		log.V(1).Infof("redis get keys failed for %v, key = %v, err: %v", dbName, keyName, err)
		return nil, nil, err
	}
	for _, key := range resp {
		alias, err := redisDb.HGet(key, "alias").Result()
		if err != nil {
			log.V(1).Infof("redis get field alias failed for %v, key = %v, err: %v", dbName, key, err)
			// clear aliasMap
			alias2name_map = make(map[string]string)
			name2alias_map = make(map[string]string)
			return nil, nil, err
		}
		alias2name_map[alias] = key[5:]
		name2alias_map[key[5:]] = alias
	}
	log.V(6).Infof("alias2nameMap: %v", alias2name_map)
	log.V(6).Infof("name2aliasMap: %v", name2alias_map)
	return alias2name_map, name2alias_map, nil
}

// Get the mapping between objects in counters DB, Ex. port name to oid in "COUNTERS_PORT_NAME_MAP" table.
// Aussuming static port name to oid map in COUNTERS table
func getCountersMap(tableName string) (map[string]string, error) {
	redisDb, _ := Target2RedisDb["COUNTERS_DB"]
	fv, err := redisDb.HGetAll(tableName).Result()
	if err != nil {
		log.V(2).Infof("redis HGetAll failed for COUNTERS_DB, tableName: %s", tableName)
		return nil, err
	}
	//log.V(6).Infof("tableName: %s, map %v", tableName, fv)
	return fv, nil
}

// Populate real data paths from paths like
// [COUNTER_DB COUNTERS Ethernet*] or [COUNTER_DB COUNTERS Ethernet68]
func v2rEthPortStats(paths []string) ([]tablePath, error) {
	separator, _ := GetTableKeySeparator(paths[DbIdx])
	var tblPaths []tablePath
	if strings.HasSuffix(paths[KeyIdx], "*") { // All Ethernet ports
		for port, oid := range countersPortNameMap {
			var oport string
			if alias, ok := name2aliasMap[port]; ok {
				oport = alias
			} else {
				log.V(2).Infof("%v does not have a vendor alias", port)
				oport = port
			}

			tblPath := tablePath{
				dbName:       paths[DbIdx],
				tableName:    paths[TblIdx],
				tableKey:     oid,
				delimitor:    separator,
				jsonTableKey: oport,
			}
			tblPaths = append(tblPaths, tblPath)
		}
	} else { //single port
		var alias, name string
		alias = paths[KeyIdx]
		name = alias
		if val, ok := alias2nameMap[alias]; ok {
			name = val
		}
		oid, ok := countersPortNameMap[name]
		if !ok {
			return nil, fmt.Errorf("%v not a valid sonic interface. Vendor alias is %v", name, alias)
		}
		tblPaths = []tablePath{{
			dbName:    paths[DbIdx],
			tableName: paths[TblIdx],
			tableKey:  oid,
			delimitor: separator,
		}}
	}
	log.V(6).Infof("v2rEthPortStats: %v", tblPaths)
	return tblPaths, nil
}

// Supported cases:
// <1> port name having suffix of "*" with specific field;
//     Ex. [COUNTER_DB COUNTERS Ethernet* SAI_PORT_STAT_PFC_0_RX_PKTS]
// <2> exact port name with specific field.
//     Ex. [COUNTER_DB COUNTERS Ethernet68 SAI_PORT_STAT_PFC_0_RX_PKTS]
// case of "*" field could be covered in v2rEthPortStats()
func v2rEthPortFieldStats(paths []string) ([]tablePath, error) {
	separator, _ := GetTableKeySeparator(paths[DbIdx])
	var tblPaths []tablePath
	if strings.HasSuffix(paths[KeyIdx], "*") {
		for port, oid := range countersPortNameMap {
			var oport string
			if alias, ok := name2aliasMap[port]; ok {
				oport = alias
			} else {
				log.V(2).Infof("%v dose not have a vendor alias", port)
				oport = port
			}

			tblPath := tablePath{
				dbName:       paths[DbIdx],
				tableName:    paths[TblIdx],
				tableKey:     oid,
				field:        paths[FieldIdx],
				delimitor:    separator,
				jsonTableKey: oport,
				jsonField:    paths[FieldIdx],
			}
			tblPaths = append(tblPaths, tblPath)
		}
	} else { //single port
		var alias, name string
		alias = paths[KeyIdx]
		name = alias
		if val, ok := alias2nameMap[alias]; ok {
			name = val
		}
		oid, ok := countersPortNameMap[name]
		if !ok {
			return nil, fmt.Errorf(" %v not a valid sonic interface. Vendor alias is %v ", name, alias)
		}
		tblPaths = []tablePath{{
			dbName:    paths[DbIdx],
			tableName: paths[TblIdx],
			tableKey:  oid,
			field:     paths[FieldIdx],
			delimitor: separator,
		}}
	}
	log.V(6).Infof("v2rEthPortFieldStats: %+v", tblPaths)
	return tblPaths, nil
}

// Populate real data paths from paths like
// [COUNTER_DB COUNTERS Ethernet* Pfcwd] or [COUNTER_DB COUNTERS Ethernet68 Pfcwd]
func v2rEthPortPfcwdStats(paths []string) ([]tablePath, error) {
	separator, _ := GetTableKeySeparator(paths[DbIdx])
	var tblPaths []tablePath
	if strings.HasSuffix(paths[KeyIdx], "*") { // Pfcwd on all Ethernet ports
		for _, pfcqueues := range countersPfcwdNameMap {
			for pfcque, oid := range pfcqueues {
				// pfcque is in format of "Interface:12"
				names := strings.Split(pfcque, separator)
				var oname string
				if alias, ok := name2aliasMap[names[0]]; ok {
					oname = alias
				} else {
					log.V(2).Infof(" %v does not have a vendor alias", names[0])
					oname = names[0]
				}
				que := strings.Join([]string{oname, names[1]}, separator)
				tblPath := tablePath{
					dbName:       paths[DbIdx],
					tableName:    paths[TblIdx],
					tableKey:     oid,
					delimitor:    separator,
					jsonTableKey: que,
				}
				tblPaths = append(tblPaths, tblPath)
			}
		}
	} else { // pfcwd counters on single port
		alias := paths[KeyIdx]
		name := alias
		if val, ok := alias2nameMap[alias]; ok {
			name = val
		}
		_, ok := countersPortNameMap[name]
		if !ok {
			return nil, fmt.Errorf("%v not a valid SONiC interface. Vendor alias is %v", name, alias)
		}

		pfcqueues, ok := countersPfcwdNameMap[name]
		if ok {
			for pfcque, oid := range pfcqueues {
				// pfcque is in format of Ethernet64:12
				names := strings.Split(pfcque, separator)
				que := strings.Join([]string{alias, names[1]}, separator)
				tblPath := tablePath{
					dbName:       paths[DbIdx],
					tableName:    paths[TblIdx],
					tableKey:     oid,
					delimitor:    separator,
					jsonTableKey: que,
				}
				tblPaths = append(tblPaths, tblPath)
			}
		}
	}
	log.V(6).Infof("v2rEthPortPfcwdStats: %v", tblPaths)
	return tblPaths, nil
}

// Populate real data paths from paths like
// [COUNTER_DB COUNTERS Ethernet* Queues] or [COUNTER_DB COUNTERS Ethernet68 Queues]
func v2rEthPortQueStats(paths []string) ([]tablePath, error) {
	separator, _ := GetTableKeySeparator(paths[DbIdx])
	var tblPaths []tablePath
	if strings.HasSuffix(paths[KeyIdx], "*") { // queues on all Ethernet ports
		for que, oid := range countersQueueNameMap {
			// que is in format of "Internal_Ethernet:12"
			names := strings.Split(que, separator)
			var oname string
			if alias, ok := name2aliasMap[names[0]]; ok {
				oname = alias
			} else {
				log.V(2).Infof(" %v dose not have a vendor alias", names[0])
				oname = names[0]
			}
			que = strings.Join([]string{oname, names[1]}, separator)
			tblPath := tablePath{
				dbName:       paths[DbIdx],
				tableName:    paths[TblIdx],
				tableKey:     oid,
				delimitor:    separator,
				jsonTableKey: que,
			}
			tblPaths = append(tblPaths, tblPath)
		}
	} else { //queues on single port
		alias := paths[KeyIdx]
		name := alias
		if val, ok := alias2nameMap[alias]; ok {
			name = val
		}
		for que, oid := range countersQueueNameMap {
			//que is in format of "Ethernet64:12"
			names := strings.Split(que, separator)
			if name != names[0] {
				continue
			}
			que = strings.Join([]string{alias, names[1]}, separator)
			tblPath := tablePath{
				dbName:       paths[DbIdx],
				tableName:    paths[TblIdx],
				tableKey:     oid,
				delimitor:    separator,
				jsonTableKey: que,
			}
			tblPaths = append(tblPaths, tblPath)
		}
	}
	log.V(6).Infof("v2rEthPortQueStats: %v", tblPaths)
	return tblPaths, nil
}

func lookupV2R(paths []string) ([]tablePath, error) {
	n, ok := v2rTrie.Find(paths)
	if ok {
		v2rTrans := n.meta.(v2rTranslate)
		return v2rTrans(paths)
	}
	return nil, fmt.Errorf("%v not found in virtual path tree", paths)
}

func init() {
	v2rTrie = NewTrie()
	v2rTrie.v2rTriePopulate()
}
