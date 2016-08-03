// Package cfg implements config's manage

package cfg

import (
	"github.com/2qif49lt/agent/cfg/cfgfile"
	"github.com/2qif49lt/utils"
	"path/filepath"

	"fmt"
	"io/ioutil"
	"os"
)

const (
	ConfigFileName = "config.toml"

	configFileDir = "config"
	certFileDir   = "cert"

	DefaultAgentdListenPort = 3567 // 默认监听端口

	DefaultUniqueAgentIdFile = "agentid"

	DeafultTlsCaFile  = "ca_cert.pem"
	DefaultTlsKeyFile = "agent_key.pem" // 作为服务器和客户端时所需的证书不一样.
	DefultTlsCertFile = "agent_cert.pem"

	// DefaultSignPubFile 参数签名
	DefaultRsaSignFile = "rsa.pem" // 用于检查调用者参数签名,agentd使用公钥,agent使用密钥.
)

var (
	configDir = os.Getenv("AGENT_CONFIG_PATH") // agent配置文件目录
	certPath  = os.Getenv("AGENT_CERT_PATH")   // 证书所在目录,默认在.agent
)

func init() {
	if configDir == "" {
		configDir = filepath.Join(utils.GetProcAbsDir(), configFileDir)
	}
	if certPath == "" {
		certPath = filepath.Join(utils.GetProcAbsDir(), certFileDir)
	}
}

// GetConfigDir returns the directory the configuration file is stored in
func GetConfigDir() string {
	return configDir
}

func GetCertPath() string {
	return certPath
}

// SetConfigDir sets the directory the configuration file is stored in
func SetConfigDir(dir string) {
	configDir = dir
}
func SetCertPath(dir string) {
	certPath = dir
}

// newcfgfile initializes an empty configuration file for the given file path 'fp'
func newcfgfile(fp string) *cfgfile.ConfigFile {
	return &cfgfile.ConfigFile{
		SrvName:  "Agentd",
		Filename: fp,
	}
}

// Load reads the configuration files
func Load() (*cfgfile.ConfigFile, error) {

	cfgfile := newcfgfile(filepath.Join(GetConfigDir(), ConfigFileName))

	if _, err := os.Stat(cfgfile.Filename); err == nil {
		file, err := os.Open(cfgfile.Filename)
		if err != nil {
			return nil, fmt.Errorf("%s - %v", cfgfile.Filename, err)
		}
		defer file.Close()

		err = cfgfile.LoadFromReader(file)
		if err != nil {
			err = fmt.Errorf("%s - %v", cfgfile.Filename, err)
			return nil, err
		}
		return cfgfile, nil
	} else {
		return nil, fmt.Errorf("%s - %v", cfgfile.Filename, err)
	}

	tmp, err := ioutil.ReadFile(filepath.Join(utils.GetProcAbsDir(), DefaultUniqueAgentIdFile))

	cfgfile.Agentid = string(tmp)

	return cfgfile, err
}

// IsTlsLegal return whether agentd install properly
func IsTlsLegal(cfg *cfgfile.ConfigFile) error {
	isexist := func(fn string) bool {
		_, err := os.Stat(filepath.Join(GetCertPath(), fn))
		return err == nil || os.IsExist(err)
	}
	if isexist(DefaultTlsKeyFile) && isexist(DefultTlsCertFile) && isexist(DefaultRsaSignFile) {
		return nil
	}
	return fmt.Errorf("cert or rsa files not exist")
}
