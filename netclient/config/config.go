package config

import (
	//"github.com/davecgh/go-spew/spew"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/gravitl/netmaker/logger"
	"github.com/gravitl/netmaker/models"
	"github.com/gravitl/netmaker/netclient/ncutils"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

// ClientConfig - struct for dealing with client configuration
type ClientConfig struct {
	Server          ServerConfig   `yaml:"server"`
	Node            models.Node    `yaml:"node"`
	NetworkSettings models.Network `yaml:"networksettings"`
	Network         string         `yaml:"network"`
	Daemon          string         `yaml:"daemon"`
	OperatingSystem string         `yaml:"operatingsystem"`
	DebugOn         bool           `yaml:"debugon"`
}

// ServerConfig - struct for dealing with the server information for a netclient
type ServerConfig struct {
	CoreDNSAddr  string `yaml:"corednsaddr"`
	GRPCAddress  string `yaml:"grpcaddress"`
	AccessKey    string `yaml:"accesskey"`
	GRPCSSL      string `yaml:"grpcssl"`
	CommsNetwork string `yaml:"commsnetwork"`
	Server       string `yaml:"server"`
}

// Write - writes the config of a client to disk
func Write(config *ClientConfig, network string) error {
	if network == "" {
		err := errors.New("no network provided - exiting")
		return err
	}
	_, err := os.Stat(ncutils.GetNetclientPath() + "/config")
	if os.IsNotExist(err) {
		os.MkdirAll(ncutils.GetNetclientPath()+"/config", 0700)
	} else if err != nil {
		return err
	}
	home := ncutils.GetNetclientPathSpecific()

	file := fmt.Sprintf(home + "netconfig-" + network)
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.ModePerm)
	if err != nil {
		return err
	}
	defer f.Close()

	err = yaml.NewEncoder(f).Encode(config)
	if err != nil {
		return err
	}
	return f.Sync()
}

// ConfigFileExists - return true if config file exists
func (config *ClientConfig) ConfigFileExists() bool {
	home := ncutils.GetNetclientPathSpecific()

	file := fmt.Sprintf(home + "netconfig-" + config.Network)
	info, err := os.Stat(file)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

// ClientConfig.ReadConfig - used to read config from client disk into memory
func (config *ClientConfig) ReadConfig() {

	nofile := false
	//home, err := homedir.Dir()
	home := ncutils.GetNetclientPathSpecific()

	file := fmt.Sprintf(home + "netconfig-" + config.Network)
	//f, err := os.Open(file)
	f, err := os.OpenFile(file, os.O_RDONLY, 0600)
	if err != nil {
		logger.Log(1, "trouble opening file: ", err.Error())
		nofile = true
		//fmt.Println("Could not access " + home + "/.netconfig,  proceeding...")
	}
	defer f.Close()

	//var cfg ClientConfig

	if !nofile {
		decoder := yaml.NewDecoder(f)
		err = decoder.Decode(&config)
		if err != nil {
			fmt.Println("no config or invalid")
			fmt.Println(err)
			log.Fatal(err)
		}
	}
}

// ModConfig - overwrites the node inside client config on disk
func ModConfig(node *models.Node) error {
	network := node.Network
	if network == "" {
		return errors.New("no network provided")
	}
	var modconfig ClientConfig
	if FileExists(ncutils.GetNetclientPathSpecific() + "netconfig-" + network) {
		useconfig, err := ReadConfig(network)
		if err != nil {
			return err
		}
		modconfig = *useconfig
	}

	modconfig.Node = (*node)
	modconfig.NetworkSettings = node.NetworkSettings
	return Write(&modconfig, network)
}

// ModConfig - overwrites the node inside client config on disk
func SaveBackup(network string) error {

	var configPath = ncutils.GetNetclientPathSpecific() + "netconfig-" + network
	var backupPath = ncutils.GetNetclientPathSpecific() + "backup.netconfig-" + network
	if FileExists(configPath) {
		input, err := os.ReadFile(configPath)
		if err != nil {
			logger.Log(0, "failed to read ", configPath, " to make a backup")
			return err
		}
		if err = os.WriteFile(backupPath, input, 0600); err != nil {
			logger.Log(0, "failed to copy backup to ", backupPath)
			return err
		}
	}
	return nil
}

// ReplaceWithBackup - replaces netconfig file with backup
func ReplaceWithBackup(network string) error {
	var backupPath = ncutils.GetNetclientPathSpecific() + "backup.netconfig-" + network
	var configPath = ncutils.GetNetclientPathSpecific() + "netconfig-" + network
	if FileExists(backupPath) {
		input, err := os.ReadFile(backupPath)
		if err != nil {
			logger.Log(0, "failed to read file ", backupPath, " to backup network: ", network)
			return err
		}
		if err = os.WriteFile(configPath, input, 0600); err != nil {
			logger.Log(0, "failed backup ", backupPath, " to ", configPath)
			return err
		}
	}
	logger.Log(0, "used backup file for network: ", network)
	return nil
}

// GetCLIConfig - gets the cli flags as a config
func GetCLIConfig(c *cli.Context) (ClientConfig, string, error) {
	var cfg ClientConfig
	if c.String("token") != "" {
		tokenbytes, err := base64.StdEncoding.DecodeString(c.String("token"))
		if err != nil {
			log.Println("error decoding token")
			return cfg, "", err
		}
		var accesstoken models.AccessToken
		if err := json.Unmarshal(tokenbytes, &accesstoken); err != nil {
			log.Println("error converting token json to object", tokenbytes)
			return cfg, "", err
		}

		if accesstoken.ServerConfig.GRPCConnString != "" {
			cfg.Server.GRPCAddress = accesstoken.ServerConfig.GRPCConnString
		}

		cfg.Network = accesstoken.ClientConfig.Network
		cfg.Node.Network = accesstoken.ClientConfig.Network
		cfg.Server.AccessKey = accesstoken.ClientConfig.Key
		cfg.Node.LocalRange = accesstoken.ClientConfig.LocalRange
		cfg.Server.GRPCSSL = accesstoken.ServerConfig.GRPCSSL
		cfg.Server.Server = accesstoken.ServerConfig.Server
		if c.String("grpcserver") != "" {
			cfg.Server.GRPCAddress = c.String("grpcserver")
		}
		if c.String("key") != "" {
			cfg.Server.AccessKey = c.String("key")
		}
		if c.String("network") != "all" {
			cfg.Network = c.String("network")
			cfg.Node.Network = c.String("network")
		}
		if c.String("localrange") != "" {
			cfg.Node.LocalRange = c.String("localrange")
		}
		if c.String("grpcssl") != "" {
			cfg.Server.GRPCSSL = c.String("grpcssl")
		}
		if c.String("corednsaddr") != "" {
			cfg.Server.CoreDNSAddr = c.String("corednsaddr")
		}

	} else {
		cfg.Server.GRPCAddress = c.String("grpcserver")
		cfg.Server.AccessKey = c.String("key")
		cfg.Network = c.String("network")
		cfg.Node.Network = c.String("network")
		cfg.Node.LocalRange = c.String("localrange")
		cfg.Server.GRPCSSL = c.String("grpcssl")
		cfg.Server.CoreDNSAddr = c.String("corednsaddr")
	}
	cfg.Node.Name = c.String("name")
	cfg.Node.Interface = c.String("interface")
	cfg.Node.Password = c.String("password")
	cfg.Node.MacAddress = c.String("macaddress")
	cfg.Node.LocalAddress = c.String("localaddress")
	cfg.Node.Address = c.String("address")
	cfg.Node.Address6 = c.String("addressIPV6")
	//cfg.Node.Roaming = c.String("roaming")
	cfg.Node.DNSOn = c.String("dnson")
	cfg.Node.IsLocal = c.String("islocal")
	cfg.Node.IsStatic = c.String("isstatic")
	cfg.Node.IsDualStack = c.String("isdualstack")
	cfg.Node.PostUp = c.String("postup")
	cfg.Node.PostDown = c.String("postdown")
	cfg.Node.ListenPort = int32(c.Int("port"))
	cfg.Node.PersistentKeepalive = int32(c.Int("keepalive"))
	cfg.Node.PublicKey = c.String("publickey")
	privateKey := c.String("privatekey")
	cfg.Node.Endpoint = c.String("endpoint")
	cfg.Node.IPForwarding = c.String("ipforwarding")
	cfg.OperatingSystem = c.String("operatingsystem")
	cfg.Daemon = c.String("daemon")
	cfg.Node.UDPHolePunch = c.String("udpholepunch")
	cfg.Node.MTU = int32(c.Int("mtu"))

	return cfg, privateKey, nil
}

// ReadConfig - reads a config of a client from disk for specified network
func ReadConfig(network string) (*ClientConfig, error) {
	if network == "" {
		err := errors.New("no network provided - exiting")
		return nil, err
	}
	nofile := false
	home := ncutils.GetNetclientPathSpecific()
	file := fmt.Sprintf(home + "netconfig-" + network)
	f, err := os.Open(file)

	if err != nil {
		if err = ReplaceWithBackup(network); err != nil {
			nofile = true
		}
		f, err = os.Open(file)
		if err != nil {
			nofile = true
		}
	}
	defer f.Close()

	var cfg ClientConfig

	if !nofile {
		decoder := yaml.NewDecoder(f)
		err = decoder.Decode(&cfg)
		if err != nil {
			fmt.Println("trouble decoding file")
			return nil, err
		}
	}
	return &cfg, err
}

// FileExists - checks if a file exists on disk
func FileExists(f string) bool {
	info, err := os.Stat(f)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

// GetNode - parses a network specified client config for node data
func GetNode(network string) models.Node {

	modcfg, err := ReadConfig(network)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	var node models.Node
	node.Fill(&modcfg.Node)

	return node
}
