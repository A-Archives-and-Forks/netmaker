package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
	"github.com/gravitl/netmaker/database"
	"github.com/gravitl/netmaker/logger"
	"github.com/gravitl/netmaker/logic"
	"github.com/gravitl/netmaker/logic/hostactions"
	"github.com/gravitl/netmaker/models"
	"github.com/gravitl/netmaker/netclient/ncutils"
	"github.com/gravitl/netmaker/servercfg"
)

// DefaultHandler default message queue handler  -- NOT USED
func DefaultHandler(client mqtt.Client, msg mqtt.Message) {
	logger.Log(0, "MQTT Message: Topic: ", string(msg.Topic()), " Message: ", string(msg.Payload()))
}

// Ping message Handler -- handles ping topic from client nodes
func Ping(client mqtt.Client, msg mqtt.Message) {
	id, err := getID(msg.Topic())
	if err != nil {
		logger.Log(0, "error getting node.ID sent on ping topic ")
		return
	}
	node, err := logic.GetNodeByID(id)
	if err != nil {
		logger.Log(3, "mq-ping error getting node: ", err.Error())
		node, err := logic.GetNodeByID(id)
		if err != nil {
			logger.Log(3, "mq-ping error getting node: ", err.Error())
			if database.IsEmptyRecord(err) {
				h := logic.GetHostByNodeID(id) // check if a host is still associated
				if h != nil {                  // inform host that node should be removed
					fakeNode := models.Node{}
					fakeNode.ID, _ = uuid.Parse(id)
					fakeNode.Action = models.NODE_DELETE
					fakeNode.PendingDelete = true
					if err := NodeUpdate(&fakeNode); err != nil {
						logger.Log(0, "failed to inform host", h.Name, h.ID.String(), "to remove node", id, err.Error())
					}
				}
			}
			return
		}
		decrypted, decryptErr := decryptMsg(&node, msg.Payload())
		if decryptErr != nil {
			logger.Log(0, "error decrypting when updating node ", node.ID.String(), decryptErr.Error())
			return
		}
		var checkin models.NodeCheckin
		if err := json.Unmarshal(decrypted, &checkin); err != nil {
			logger.Log(1, "error unmarshaling payload ", err.Error())
			return
		}
		host, err := logic.GetHost(node.HostID.String())
		if err != nil {
			logger.Log(0, "error retrieving host for node ", node.ID.String(), err.Error())
			return
		}
		node.SetLastCheckIn()
		host.Version = checkin.Version
		node.Connected = checkin.Connected
		host.Interfaces = checkin.Ifaces
		for i := range host.Interfaces {
			host.Interfaces[i].AddressString = host.Interfaces[i].Address.String()
		}
		if err := logic.UpdateNode(&node, &node); err != nil {
			logger.Log(0, "error updating node", node.ID.String(), " on checkin", err.Error())
			return
		}

		return
	}
	decrypted, decryptErr := decryptMsg(&node, msg.Payload())
	if decryptErr != nil {
		logger.Log(0, "error decrypting when updating node ", node.ID.String(), decryptErr.Error())
		return
	}
	var checkin models.NodeCheckin
	if err := json.Unmarshal(decrypted, &checkin); err != nil {
		logger.Log(1, "error unmarshaling payload ", err.Error())
		return
	}
	host, err := logic.GetHost(node.HostID.String())
	if err != nil {
		logger.Log(0, "error retrieving host for node ", node.ID.String(), err.Error())
		return
	}
	node.SetLastCheckIn()
	host.Version = checkin.Version
	node.Connected = checkin.Connected
	host.Interfaces = checkin.Ifaces
	for i := range host.Interfaces {
		host.Interfaces[i].AddressString = host.Interfaces[i].Address.String()
	}
	if err := logic.UpdateNode(&node, &node); err != nil {
		logger.Log(0, "error updating node", node.ID.String(), " on checkin", err.Error())
		return
	}

	logger.Log(3, "ping processed for node", node.ID.String())
	// --TODO --set client version once feature is implemented.
	//node.SetClientVersion(msg.Payload())
}

// UpdateNode  message Handler -- handles updates from client nodes
func UpdateNode(client mqtt.Client, msg mqtt.Message) {
	id, err := getID(msg.Topic())
	if err != nil {
		logger.Log(1, "error getting node.ID sent on ", msg.Topic(), err.Error())
		return
	}
	currentNode, err := logic.GetNodeByID(id)
	if err != nil {
		logger.Log(1, "error getting node ", id, err.Error())
		return
	}
	decrypted, decryptErr := decryptMsg(&currentNode, msg.Payload())
	if decryptErr != nil {
		logger.Log(1, "failed to decrypt message for node ", id, decryptErr.Error())
		return
	}
	var newNode models.Node
	if err := json.Unmarshal(decrypted, &newNode); err != nil {
		logger.Log(1, "error unmarshaling payload ", err.Error())
		return
	}

	ifaceDelta := logic.IfaceDelta(&currentNode, &newNode)
	if servercfg.Is_EE && ifaceDelta {
		if err = logic.EnterpriseResetAllPeersFailovers(currentNode.ID, currentNode.Network); err != nil {
			logger.Log(1, "failed to reset failover list during node update", currentNode.ID.String(), currentNode.Network)
		}
	}
	newNode.SetLastCheckIn()
	if err := logic.UpdateNode(&currentNode, &newNode); err != nil {
		logger.Log(1, "error saving node", err.Error())
		return
	}
	if ifaceDelta { // reduce number of unneeded updates, by only sending on iface changes
		if err = PublishPeerUpdate(); err != nil {
			logger.Log(0, "error updating peers when node", currentNode.ID.String(), "informed the server of an interface change", err.Error())
		}
	}

	logger.Log(1, "updated node", id, newNode.ID.String())
}

// UpdateHost  message Handler -- handles host updates from clients
func UpdateHost(client mqtt.Client, msg mqtt.Message) {
	id, err := getID(msg.Topic())
	if err != nil {
		logger.Log(1, "error getting host.ID sent on ", msg.Topic(), err.Error())
		return
	}
	currentHost, err := logic.GetHost(id)
	if err != nil {
		logger.Log(1, "error getting host ", id, err.Error())
		return
	}
	decrypted, decryptErr := decryptMsgWithHost(currentHost, msg.Payload())
	if decryptErr != nil {
		logger.Log(1, "failed to decrypt message for host ", id, decryptErr.Error())
		return
	}
	var hostUpdate models.HostUpdate
	if err := json.Unmarshal(decrypted, &hostUpdate); err != nil {
		logger.Log(1, "error unmarshaling payload ", err.Error())
		return
	}
	logger.Log(3, fmt.Sprintf("recieved host update: %s\n", hostUpdate.Host.ID.String()))
	var sendPeerUpdate bool
	switch hostUpdate.Action {
	case models.Acknowledgement:
		hu := hostactions.GetAction(currentHost.ID.String())
		if hu != nil {
			if err = HostUpdate(hu); err != nil {
				logger.Log(0, "failed to send new node to host", hostUpdate.Host.Name, currentHost.ID.String(), err.Error())
				return
			} else {
				if err = PublishSingleHostPeerUpdate(context.Background(), currentHost, nil, nil); err != nil {
					logger.Log(0, "failed peers publish after join acknowledged", hostUpdate.Host.Name, currentHost.ID.String(), err.Error())
					return
				}
				if err = handleNewNodeDNS(&hu.Host, &hu.Node); err != nil {
					logger.Log(0, "failed to send dns update after node,", hu.Node.ID.String(), ", added to host", hu.Host.Name, err.Error())
					return
				}
			}
		}
	case models.UpdateHost:
		sendPeerUpdate = logic.UpdateHostFromClient(&hostUpdate.Host, currentHost)
		err := logic.UpsertHost(currentHost)
		if err != nil {
			logger.Log(0, "failed to update host: ", currentHost.ID.String(), err.Error())
			return
		}
	case models.DeleteHost:
		if servercfg.GetBrokerType() == servercfg.EmqxBrokerType {
			// delete EMQX credentials for host
			if err := DeleteEmqxUser(currentHost.ID.String()); err != nil {
				logger.Log(0, "failed to remove host credentials from EMQX: ", currentHost.ID.String(), err.Error())
				return
			}
		}
		if err := logic.DisassociateAllNodesFromHost(currentHost.ID.String()); err != nil {
			logger.Log(0, "failed to delete all nodes of host: ", currentHost.ID.String(), err.Error())
			return
		}
		if err := logic.RemoveHostByID(currentHost.ID.String()); err != nil {
			logger.Log(0, "failed to delete host: ", currentHost.ID.String(), err.Error())
			return
		}
		sendPeerUpdate = true
	}

	if sendPeerUpdate {
		err := PublishPeerUpdate()
		if err != nil {
			logger.Log(0, "failed to pulish peer update: ", err.Error())
		}
	}
	// if servercfg.Is_EE && ifaceDelta {
	// 	if err = logic.EnterpriseResetAllPeersFailovers(currentHost.ID.String(), currentHost.Network); err != nil {
	// 		logger.Log(1, "failed to reset failover list during node update", currentHost.ID.String(), currentHost.Network)
	// 	}
	// }
}

// UpdateMetrics  message Handler -- handles updates from client nodes for metrics
func UpdateMetrics(client mqtt.Client, msg mqtt.Message) {
	if servercfg.Is_EE {
		id, err := getID(msg.Topic())
		if err != nil {
			logger.Log(1, "error getting node.ID sent on ", msg.Topic(), err.Error())
			return
		}
		currentNode, err := logic.GetNodeByID(id)
		if err != nil {
			logger.Log(1, "error getting node ", id, err.Error())
			return
		}
		decrypted, decryptErr := decryptMsg(&currentNode, msg.Payload())
		if decryptErr != nil {
			logger.Log(1, "failed to decrypt message for node ", id, decryptErr.Error())
			return
		}

		var newMetrics models.Metrics
		if err := json.Unmarshal(decrypted, &newMetrics); err != nil {
			logger.Log(1, "error unmarshaling payload ", err.Error())
			return
		}

		shouldUpdate := updateNodeMetrics(&currentNode, &newMetrics)

		if err = logic.UpdateMetrics(id, &newMetrics); err != nil {
			logger.Log(1, "faield to update node metrics", id, err.Error())
			return
		}
		if servercfg.IsMetricsExporter() {
			if err := pushMetricsToExporter(newMetrics); err != nil {
				logger.Log(2, fmt.Sprintf("failed to push node: [%s] metrics to exporter, err: %v",
					currentNode.ID, err))
			}
		}

		if newMetrics.Connectivity != nil {
			err := logic.EnterpriseFailoverFunc(&currentNode)
			if err != nil {
				logger.Log(0, "failed to failover for node", currentNode.ID.String(), "on network", currentNode.Network, "-", err.Error())
			}
		}

		if shouldUpdate {
			logger.Log(2, "updating peers after node", currentNode.ID.String(), currentNode.Network, "detected connectivity issues")
			host, err := logic.GetHost(currentNode.HostID.String())
			if err == nil {
				if err = PublishSingleHostPeerUpdate(context.Background(), host, nil, nil); err != nil {
					logger.Log(0, "failed to publish update after failover peer change for node", currentNode.ID.String(), currentNode.Network)
				}
			}
		}

		logger.Log(1, "updated node metrics", id)
	}
}

// ClientPeerUpdate  message handler -- handles updating peers after signal from client nodes
func ClientPeerUpdate(client mqtt.Client, msg mqtt.Message) {
	id, err := getID(msg.Topic())
	if err != nil {
		logger.Log(1, "error getting node.ID sent on ", msg.Topic(), err.Error())
		return
	}
	currentNode, err := logic.GetNodeByID(id)
	if err != nil {
		logger.Log(1, "error getting node ", id, err.Error())
		return
	}
	decrypted, decryptErr := decryptMsg(&currentNode, msg.Payload())
	if decryptErr != nil {
		logger.Log(1, "failed to decrypt message during client peer update for node ", id, decryptErr.Error())
		return
	}
	switch decrypted[0] {
	case ncutils.ACK:
		// do we still need this
	case ncutils.DONE:
		if err = PublishPeerUpdate(); err != nil {
			logger.Log(1, "error publishing peer update for node", currentNode.ID.String(), err.Error())
			return
		}
	}

	logger.Log(1, "sent peer updates after signal received from", id)
}

func updateNodeMetrics(currentNode *models.Node, newMetrics *models.Metrics) bool {
	if newMetrics.FailoverPeers == nil {
		newMetrics.FailoverPeers = make(map[string]string)
	}
	oldMetrics, err := logic.GetMetrics(currentNode.ID.String())
	if err != nil {
		logger.Log(1, "error finding old metrics for node", currentNode.ID.String())
		return false
	}
	if oldMetrics.FailoverPeers == nil {
		oldMetrics.FailoverPeers = make(map[string]string)
	}

	var attachedClients []models.ExtClient
	if currentNode.IsIngressGateway {
		clients, err := logic.GetExtClientsByID(currentNode.ID.String(), currentNode.Network)
		if err == nil {
			attachedClients = clients
		}
	}
	if len(attachedClients) > 0 {
		// associate ext clients with IDs
		for i := range attachedClients {
			extMetric := newMetrics.Connectivity[attachedClients[i].PublicKey]
			if len(extMetric.NodeName) == 0 &&
				len(newMetrics.Connectivity[attachedClients[i].ClientID].NodeName) > 0 { // cover server clients
				extMetric = newMetrics.Connectivity[attachedClients[i].ClientID]
				if extMetric.TotalReceived > 0 && extMetric.TotalSent > 0 {
					extMetric.Connected = true
				}
			}
			extMetric.NodeName = attachedClients[i].ClientID
			delete(newMetrics.Connectivity, attachedClients[i].PublicKey)
			newMetrics.Connectivity[attachedClients[i].ClientID] = extMetric
		}
	}

	// run through metrics for each peer
	for k := range newMetrics.Connectivity {
		currMetric := newMetrics.Connectivity[k]
		oldMetric := oldMetrics.Connectivity[k]
		currMetric.TotalTime += oldMetric.TotalTime
		currMetric.Uptime += oldMetric.Uptime // get the total uptime for this connection
		if currMetric.CollectedByProxy {
			currMetric.TotalReceived += oldMetric.TotalReceived
			currMetric.TotalSent += oldMetric.TotalSent
		} else {
			if currMetric.TotalReceived < oldMetric.TotalReceived {
				currMetric.TotalReceived += oldMetric.TotalReceived
			} else {
				currMetric.TotalReceived += int64(math.Abs(float64(currMetric.TotalReceived) - float64(oldMetric.TotalReceived)))
			}
			if currMetric.TotalSent < oldMetric.TotalSent {
				currMetric.TotalSent += oldMetric.TotalSent
			} else {
				currMetric.TotalSent += int64(math.Abs(float64(currMetric.TotalSent) - float64(oldMetric.TotalSent)))
			}
		}
		if currMetric.Uptime == 0 || currMetric.TotalTime == 0 {
			currMetric.PercentUp = 0
		} else {
			currMetric.PercentUp = 100.0 * (float64(currMetric.Uptime) / float64(currMetric.TotalTime))
		}
		totalUpMinutes := currMetric.Uptime * ncutils.CheckInInterval
		currMetric.ActualUptime = time.Duration(totalUpMinutes) * time.Minute
		delete(oldMetrics.Connectivity, k) // remove from old data
		newMetrics.Connectivity[k] = currMetric

	}

	// add nodes that need failover
	nodes, err := logic.GetNetworkNodes(currentNode.Network)
	if err != nil {
		logger.Log(0, "failed to retrieve nodes while updating metrics")
		return false
	}
	for _, node := range nodes {
		if !newMetrics.Connectivity[node.ID.String()].Connected &&
			len(newMetrics.Connectivity[node.ID.String()].NodeName) > 0 &&
			node.Connected &&
			len(node.FailoverNode) > 0 &&
			!node.Failover {
			newMetrics.FailoverPeers[node.ID.String()] = node.FailoverNode.String()
		}
	}
	shouldUpdate := len(oldMetrics.FailoverPeers) == 0 && len(newMetrics.FailoverPeers) > 0
	for k, v := range oldMetrics.FailoverPeers {
		if len(newMetrics.FailoverPeers[k]) > 0 && len(v) == 0 {
			shouldUpdate = true
		}

		if len(v) > 0 && len(newMetrics.FailoverPeers[k]) == 0 {
			newMetrics.FailoverPeers[k] = v
		}
	}

	for k := range oldMetrics.Connectivity { // cleanup any left over data, self healing
		delete(newMetrics.Connectivity, k)
	}
	return shouldUpdate
}

func handleNewNodeDNS(host *models.Host, node *models.Node) error {
	dns := models.DNSUpdate{
		Action: models.DNSInsert,
		Name:   host.Name + "." + node.Network,
	}
	if node.Address.IP != nil {
		dns.Address = node.Address.IP.String()
		if err := PublishDNSUpdate(node.Network, dns); err != nil {
			return err
		}
	} else if node.Address6.IP != nil {
		dns.Address = node.Address6.IP.String()
		if err := PublishDNSUpdate(node.Network, dns); err != nil {
			return err
		}
	}
	if err := PublishAllDNS(node); err != nil {
		return err
	}
	return nil
}
