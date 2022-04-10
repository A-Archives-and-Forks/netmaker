package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"sync"
	"syscall"

	"github.com/gravitl/netmaker/auth"
	"github.com/gravitl/netmaker/config"
	controller "github.com/gravitl/netmaker/controllers"
	"github.com/gravitl/netmaker/database"
	"github.com/gravitl/netmaker/functions"
	nodepb "github.com/gravitl/netmaker/grpc"
	"github.com/gravitl/netmaker/logger"
	"github.com/gravitl/netmaker/logic"
	"github.com/gravitl/netmaker/models"
	"github.com/gravitl/netmaker/mq"
	"github.com/gravitl/netmaker/netclient/ncutils"
	"github.com/gravitl/netmaker/servercfg"
	"github.com/gravitl/netmaker/serverctl"
	"github.com/gravitl/netmaker/tls"
	"google.golang.org/grpc"
)

var version = "dev"

// Start DB Connection and start API Request Handler
func main() {
	absoluteConfigPath := flag.String("c", "", "absolute path to configuration file")
	flag.Parse()

	setupConfig(*absoluteConfigPath)
	servercfg.SetVersion(version)
	fmt.Println(models.RetrieveLogo()) // print the logo
	initialize()
	logger.Log(2, "inialization complete") // initial db and grpc server
	setGarbageCollection()
	defer database.CloseDB()
	startControllers() // start the grpc or rest endpoints
}

func setupConfig(absoluteConfigPath string) {
	if len(absoluteConfigPath) > 0 {
		cfg, err := config.ReadConfig(absoluteConfigPath)
		if err != nil {
			logger.Log(0, fmt.Sprintf("failed parsing config at: %s", absoluteConfigPath))
			return
		}
		config.Config = cfg
	}
}

func initialize() { // Client Mode Prereq Check
	var err error

	if servercfg.GetMasterKey() == "" {
		logger.Log(0, "warning: MASTER_KEY not set, this could make account recovery difficult")
	}

	if servercfg.GetNodeID() == "" {
		logger.FatalLog("error: must set NODE_ID, currently blank")
	}

	if err = database.InitializeDatabase(); err != nil {
		logger.FatalLog("Error connecting to database")
	}
	logger.Log(0, "database successfully connected")
	logic.SetJWTSecret()

	err = logic.TimerCheckpoint()
	if err != nil {
		logger.Log(1, "Timer error occurred: ", err.Error())
	}
	var authProvider = auth.InitializeAuthProvider()
	if authProvider != "" {
		logger.Log(0, "OAuth provider,", authProvider+",", "initialized")
	} else {
		logger.Log(0, "no OAuth provider found or not configured, continuing without OAuth")
	}

	err = serverctl.SetDefaultACLS()
	if err != nil {
		logger.FatalLog("error setting default acls: ", err.Error())
	}

	if servercfg.IsClientMode() != "off" {
		output, err := ncutils.RunCmd("id -u", true)
		if err != nil {
			logger.FatalLog("Error running 'id -u' for prereq check. Please investigate or disable client mode.", output, err.Error())
		}
		uid, err := strconv.Atoi(string(output[:len(output)-1]))
		if err != nil {
			logger.FatalLog("Error retrieving uid from 'id -u' for prereq check. Please investigate or disable client mode.", err.Error())
		}
		if uid != 0 {
			logger.FatalLog("To run in client mode requires root privileges. Either disable client mode or run with sudo.")
		}
		if err := serverctl.InitServerNetclient(); err != nil {
			logger.FatalLog("Did not find netclient to use CLIENT_MODE")
		}
	}
	// initialize iptables to ensure gateways work correctly and mq is forwarded if containerized
	if servercfg.ManageIPTables() != "off" {
		if err = serverctl.InitIPTables(true); err != nil {
			logger.FatalLog("Unable to initialize iptables on host:", err.Error())
		}
	}

	if servercfg.IsDNSMode() {
		err := functions.SetDNSDir()
		if err != nil {
			logger.FatalLog(err.Error())
		}
	}
	if servercfg.GetServerName() == "" {
		logger.FatalLog("Server Name not set")
	}
	logger.Log(2, "Checking certificates")
	checkCertificates(servercfg.GetServerName())
}

func startControllers() {
	var waitnetwork sync.WaitGroup
	//Run Agent Server
	if servercfg.IsAgentBackend() {
		if !(servercfg.DisableRemoteIPCheck()) && servercfg.GetGRPCHost() == "127.0.0.1" {
			err := servercfg.SetHost()
			if err != nil {
				logger.FatalLog("Unable to Set host. Exiting...", err.Error())
			}
		}
		waitnetwork.Add(1)
		go runGRPC(&waitnetwork)
	}

	if servercfg.IsDNSMode() {
		err := logic.SetDNS()
		if err != nil {
			logger.Log(0, "error occurred initializing DNS: ", err.Error())
		}
	}
	//Run Rest Server
	if servercfg.IsRestBackend() {
		if !servercfg.DisableRemoteIPCheck() && servercfg.GetAPIHost() == "127.0.0.1" {
			err := servercfg.SetHost()
			if err != nil {
				logger.FatalLog("Unable to Set host. Exiting...", err.Error())
			}
		}
		waitnetwork.Add(1)
		go controller.HandleRESTRequests(&waitnetwork)
	}

	//Run MessageQueue
	if servercfg.IsMessageQueueBackend() {
		waitnetwork.Add(1)
		go runMessageQueue(&waitnetwork)
	}

	if !servercfg.IsAgentBackend() && !servercfg.IsRestBackend() && !servercfg.IsMessageQueueBackend() {
		logger.Log(0, "No Server Mode selected, so nothing is being served! Set Agent mode (AGENT_BACKEND) or Rest mode (REST_BACKEND) or MessageQueue (MESSAGEQUEUE_BACKEND) to 'true'.")
	}

	waitnetwork.Wait()
}

func runGRPC(wg *sync.WaitGroup) {

	defer wg.Done()

	grpcport := servercfg.GetGRPCPort()

	listener, err := net.Listen("tcp", ":"+grpcport)
	// Handle errors if any
	if err != nil {
		logger.FatalLog("[netmaker] Unable to listen on port", grpcport, ": error:", err.Error())
	}

	s := grpc.NewServer(
		authServerUnaryInterceptor(),
	)
	// Create NodeService type
	srv := &controller.NodeServiceServer{}

	// Register the service with the server
	nodepb.RegisterNodeServiceServer(s, srv)

	// Start the server in a child routine
	go func() {
		if err := s.Serve(listener); err != nil {
			logger.FatalLog("Failed to serve:", err.Error())
		}
	}()
	logger.Log(0, "Agent Server successfully started on port ", grpcport, "(gRPC)")

	// Relay os.Interrupt to our channel (os.Interrupt = CTRL+C)
	// Ignore other incoming signals
	ctx, stop := signal.NotifyContext(context.TODO(), os.Interrupt)
	defer stop()

	// Block main routine until a signal is received
	// As long as user doesn't press CTRL+C a message is not passed and our main routine keeps running
	<-ctx.Done()

	// After receiving CTRL+C Properly stop the server
	logger.Log(0, "Stopping the Agent server...")
	s.GracefulStop()
	listener.Close()
	logger.Log(0, "Agent server closed..")
	logger.Log(0, "Closed DB connection.")
}

// Should we be using a context vice a waitgroup????????????
func runMessageQueue(wg *sync.WaitGroup) {
	defer wg.Done()
	logger.Log(0, "connecting to mq broker at", servercfg.GetMessageQueueEndpoint(false))
	var client = mq.SetupMQTT(false) // Set up the subscription listener
	ctx, cancel := context.WithCancel(context.Background())
	go mq.Keepalive(ctx)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, os.Interrupt)
	<-quit
	cancel()
	logger.Log(0, "Message Queue shutting down")
	client.Disconnect(250)
}

func authServerUnaryInterceptor() grpc.ServerOption {
	return grpc.UnaryInterceptor(controller.AuthServerUnaryInterceptor)
}

func setGarbageCollection() {
	_, gcset := os.LookupEnv("GOGC")
	if !gcset {
		debug.SetGCPercent(ncutils.DEFAULT_GC_PERCENT)
	}
}

func checkCertificates(server string) {
	var key *ed25519.PrivateKey
	var ca *x509.Certificate
	if _, err := os.Stat("/etc/netmaker/root.pem"); errors.Is(err, os.ErrNotExist) {
		logger.Log(2, "generating root CA", err.Error())
		key, ca, err = generateRootCA()
		if err != nil {
			logger.FatalLog("root-ca failure ", err.Error())
		}
	}
	if _, err := os.Stat("/etc/netmaker/" + server + "/server.pem"); errors.Is(err, os.ErrNotExist) {
		logger.Log(2, "generating server certificate")
		_, err := generateCertificate(server, key, ca)
		if err != nil {
			logger.FatalLog("server cert failure ", err.Error())
		}
	}
}

func generateRootCA() (*ed25519.PrivateKey, *x509.Certificate, error) {
	key := tls.NewKey()
	privateKey, err := key.Ed25519PrivateKey()
	if err != nil {
		return nil, nil, fmt.Errorf("private key %w", err)
	}
	name := tls.NewCName("Gravitl")
	csr, err := tls.NewCSR(privateKey, name)
	if err != nil {
		return nil, nil, fmt.Errorf("csr %w", err)
	}
	ca, err := tls.SelfSignedCA(privateKey, csr, 365)
	if err != nil {
		return nil, nil, fmt.Errorf("self signed cert %w", err)
	}
	logger.Log(2, "Saving root key")
	if err := tls.SaveKey("/etc/netmaker/", "root.key", privateKey); err != nil {
		return nil, nil, fmt.Errorf("save key %w", err)
	}
	logger.Log(2, "Saving root ca")
	if err := tls.SaveCert("/etc/netmaker/", "root.pem", ca); err != nil {
		return nil, nil, fmt.Errorf("save root ca %w", err)
	}
	return &privateKey, ca, nil
}

func generateCertificate(server string, key *ed25519.PrivateKey, ca *x509.Certificate) (*x509.Certificate, error) {
	name := tls.NewName(server, "", "Netmaker")
	csr, err := tls.NewCSR(*key, name)
	if err != nil {
		return nil, fmt.Errorf("csr %w", err)
	}
	cert, err := tls.NewEndEntityCert(*key, csr, ca, 365)
	if err != nil {
		return nil, fmt.Errorf("cert %w", err)
	}
	logger.Log(2, "saving server cert")
	if err := tls.SaveCert("/etc/netmaker/"+server+"/", "/server.pem", cert); err != nil {
		return nil, fmt.Errorf("save cert %w", err)
	}
	return cert, nil
}
