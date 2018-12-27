
// The function of this module is to connect ovsdb and disconnect ovsdb,
//remove port from brint.

package port

import (
	"fmt"
	"strings"
	"github.com/socketplane/libovsdb"
	log "github.com/Sirupsen/logrus"
)

type OVSConnection struct {
	ovsdbClient         *libovsdb.OvsdbClient
}

type OVSDB struct {
	UUID     string
	Name     string
	Mac		 string
}

// Constants for OVSDB table names
const (
	bridgeTable    = "Bridge"
	portTable      = "Port"
	interfaceTable = "Interface"
	bridgeName     = "br-int"
	OvsDBName      = "Open_vSwitch"
)

const (
	ZtePortTableColumnName = "name"
)
// AddPortToBrint adds Zte port to br-int bridge
func (ovsConnection *OVSConnection)AddPortToBrint(intfName string, ovsdb OVSDB) error {
	namedPortUUID := "port"
	namedIntfUUID := "intf"
	var err error
	// 1) Insert a row for Zte port in OVSDB Interface table
	extIDMap := make(map[string]string)
	intfOp := libovsdb.Operation{}
	intf := make(map[string]interface{})
	intf["name"] = intfName
	extIDMap["iface-id"] = ovsdb.UUID
	extIDMap["iface-status"] = "active"
	extIDMap["attached-mac"] = ovsdb.Mac
	extIDMap["vm-uuid=k8s-"] = intfName
	intf["external_ids"], err = libovsdb.NewOvsMap(extIDMap)
	if err != nil {
		return err
	}
	// interface table ops
	intfOp = libovsdb.Operation{
		Op:       "insert",
		Table:    interfaceTable,
		Row:      intf,
		UUIDName: namedIntfUUID,
	}
	log.Debugf("Successfully insert a interface table %s", intfOp)
	// 2) Insert a row for Zte port in OVSDB Port table
	portOp := libovsdb.Operation{}
	port := make(map[string]interface{})
	port["name"] = intfName
	port["interfaces"] = libovsdb.UUID{namedIntfUUID}
	port["external_ids"], err = libovsdb.NewOvsMap(extIDMap)
	if err != nil {
		return err
	}
	portOp = libovsdb.Operation{
		Op:       "insert",
		Table:    portTable,
		Row:      port,
		UUIDName: namedPortUUID,
	}
	log.Debugf("Successfully insert a port table %s", portOp)
	// 3) Mutate the Ports column of the row in the Bridge table with new Zte port
	mutateUUID := []libovsdb.UUID{libovsdb.UUID{namedPortUUID}}
	mutateSet, _ := libovsdb.NewOvsSet(mutateUUID)
	mutation := libovsdb.NewMutation("ports", "insert", mutateSet)
	condition := libovsdb.NewCondition("name", "==", bridgeName)
	mutateOp := libovsdb.Operation{
		Op:        "mutate",
		Table:     bridgeTable,
		Mutations: []interface{}{mutation},
		Where:     []interface{}{condition},
	}
	log.Debugf("Successfully insert a bridge table %s", mutateOp)
	operations := []libovsdb.Operation{intfOp, portOp, mutateOp}
	reply, err := ovsConnection.ovsdbClient.Transact(OvsDBName, operations...)
	log.Debugf("Receive ovsdb reply %s", reply)
	if err != nil || len(reply) < len(operations) {
		return fmt.Errorf("Problem mutating row in the OVSDB Bridge table for Brint")
	}
	return nil
}

// NewUnixSocketConnection creates a connection to the OVS Server using Unix sockets
func NewUnixSocketConnection(socketfile string) (OVSConnection, error) {
	var ovsConnection OVSConnection
	var err error
	if ovsConnection.ovsdbClient, err = libovsdb.ConnectWithUnixSocket(socketfile); err != nil {
		return ovsConnection, err
	}
	return ovsConnection, err
}

// RemovePortFromBrint will remove a port from br-int bridge
func (ovsConnection *OVSConnection) RemovePortFromBrint(portName string) error {
	condition := libovsdb.NewCondition("name", "==", portName)
	selectOp := libovsdb.Operation{
		Op:    "select",
		Table: "Port",
		Where: []interface{}{condition},
	}
	selectOperation := []libovsdb.Operation{selectOp}
	reply, err := ovsConnection.ovsdbClient.Transact(OvsDBName, selectOperation...)
	if err != nil || len(reply) != 1 || len(reply[0].Rows) != 1 {
		return fmt.Errorf("Problem selecting row in the OVSDB Port table for Brint")
	}
	// Obtain Port table OVSDB row corresponding to the port name
	ovsdbRow := reply[0].Rows[0]
	portUUID := ovsdbRow["_uuid"]
	portUUIDStr := fmt.Sprintf("%v", portUUID)
	//portUUIDNew := util.SplitUUIDString(portUUIDStr)
	portUUIDNew := SplitUUIDString(portUUIDStr)
	condition = libovsdb.NewCondition("name", "==", portName)
	deleteOp := libovsdb.Operation{
		Op:    "delete",
		Table: "Port",
		Where: []interface{}{condition},
	}
	// Deleting a Bridge row in Bridge table requires mutating the open_vswitch table.
	mutateUUID := []libovsdb.UUID{libovsdb.UUID{portUUIDNew}}
	mutateSet, _ := libovsdb.NewOvsSet(mutateUUID)
	mutation := libovsdb.NewMutation("ports", "delete", mutateSet)
	condition = libovsdb.NewCondition("name", "==", bridgeName)
	// simple mutate operation
	mutateOp := libovsdb.Operation{
		Op:        "mutate",
		Table:     "Bridge",
		Mutations: []interface{}{mutation},
		Where:     []interface{}{condition},
	}
	operations := []libovsdb.Operation{deleteOp, mutateOp}
	reply, err = ovsConnection.ovsdbClient.Transact(OvsDBName, operations...)
	if err != nil || len(reply) < len(operations) {
		return fmt.Errorf("Problem mutating row in the OVSDB Bridge table for Brint")
	}
	return nil
}

func (ovsConnection OVSConnection) Disconnect() {
	ovsConnection.ovsdbClient.Disconnect()
}

func SplitUUIDString(uuid string) string {
	uuidName := strings.Split(uuid, "[")
	uuidStrSplit := strings.Split(uuidName[1], "]")
	uuidStr := strings.Split(uuidStrSplit[0], " ")
	return uuidStr[1]
}