package mq

import (
	"context"
	"fmt"
	"log"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gravitl/netmaker/logger"
	"github.com/gravitl/netmaker/logic"
	"github.com/gravitl/netmaker/servercfg"
)

// KEEPALIVE_TIMEOUT - time in seconds for timeout
const KEEPALIVE_TIMEOUT = 60 //timeout in seconds
// MQ_DISCONNECT - disconnects MQ
const MQ_DISCONNECT = 250

// MQ_TIMEOUT - timeout for MQ
const MQ_TIMEOUT = 30

var peer_force_send = 0

var mqclient mqtt.Client

func setMqOptions(user, password string, opts *mqtt.ClientOptions) {
	broker, _ := servercfg.GetMessageQueueEndpoint()
	opts.AddBroker(broker)
	id := logic.RandomString(23)
	opts.ClientID = id
	opts.SetUsername(user)
	opts.SetPassword(password)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(time.Second << 2)
	opts.SetKeepAlive(time.Minute)
	opts.SetWriteTimeout(time.Minute)
}

// SetupMQTT creates a connection to broker and return client
func SetupMQTT() {
	if servercfg.GetBrokerType() == servercfg.EmqxBrokerType {
		time.Sleep(10 * time.Second) // wait for the REST endpoint to be ready
		// setup authenticator and create admin user
		if err := CreateEmqxDefaultAuthenticator(); err != nil {
			logger.Log(0, err.Error())
		}
		DeleteEmqxUser(servercfg.GetMqUserName())
		if err := CreateEmqxUser(servercfg.GetMqUserName(), servercfg.GetMqPassword(), true); err != nil {
			log.Fatal(err)
		}
		// create an ACL authorization source for the built in EMQX MNESIA database
		if err := CreateEmqxDefaultAuthorizer(); err != nil {
			logger.Log(0, err.Error())
		}
		// create a default deny ACL to all topics for all users
		if err := CreateDefaultDenyRule(); err != nil {
			log.Fatal(err)
		}
	}
	opts := mqtt.NewClientOptions()
	setMqOptions(servercfg.GetMqUserName(), servercfg.GetMqPassword(), opts)
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		serverName := servercfg.GetServer()
		if token := client.Subscribe(fmt.Sprintf("update/%s/#", serverName), 0, mqtt.MessageHandler(UpdateNode)); token.WaitTimeout(MQ_TIMEOUT*time.Second) && token.Error() != nil {
			client.Disconnect(240)
			logger.Log(0, "node update subscription failed")
		}
		if token := client.Subscribe(fmt.Sprintf("host/serverupdate/%s/#", serverName), 0, mqtt.MessageHandler(UpdateHost)); token.WaitTimeout(MQ_TIMEOUT*time.Second) && token.Error() != nil {
			client.Disconnect(240)
			logger.Log(0, "host update subscription failed")
		}
		if token := client.Subscribe(fmt.Sprintf("signal/%s/#", serverName), 0, mqtt.MessageHandler(ClientPeerUpdate)); token.WaitTimeout(MQ_TIMEOUT*time.Second) && token.Error() != nil {
			client.Disconnect(240)
			logger.Log(0, "node client subscription failed")
		}
		if token := client.Subscribe(fmt.Sprintf("metrics/%s/#", serverName), 0, mqtt.MessageHandler(UpdateMetrics)); token.WaitTimeout(MQ_TIMEOUT*time.Second) && token.Error() != nil {
			client.Disconnect(240)
			logger.Log(0, "node metrics subscription failed")
		}

		opts.SetOrderMatters(true)
		opts.SetResumeSubs(true)
	})
	mqclient = mqtt.NewClient(opts)
	tperiod := time.Now().Add(10 * time.Second)
	for {
		if token := mqclient.Connect(); !token.WaitTimeout(MQ_TIMEOUT*time.Second) || token.Error() != nil {
			logger.Log(2, "unable to connect to broker, retrying ...")
			if time.Now().After(tperiod) {
				if token.Error() == nil {
					logger.FatalLog("could not connect to broker, token timeout, exiting ...")
				} else {
					logger.FatalLog("could not connect to broker, exiting ...", token.Error().Error())
				}
			}
		} else {
			break
		}
		time.Sleep(2 * time.Second)
	}
}

// Keepalive -- periodically pings all nodes to let them know server is still alive and doing well
func Keepalive(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second * KEEPALIVE_TIMEOUT):
			sendPeers()
		}
	}
}

// IsConnected - function for determining if the mqclient is connected or not
func IsConnected() bool {
	return mqclient != nil && mqclient.IsConnected()
}

// CloseClient - function to close the mq connection from server
func CloseClient() {
	mqclient.Disconnect(250)
}
