package sftpd

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pires/go-proxyproto"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/drakkan/sftpgo/dataprovider"
	"github.com/drakkan/sftpgo/logger"
	"github.com/drakkan/sftpgo/metrics"
	"github.com/drakkan/sftpgo/utils"
)

const (
	defaultPrivateRSAKeyName    = "id_rsa"
	defaultPrivateECDSAKeyName  = "id_ecdsa"
	sourceAddressCriticalOption = "source-address"
)

var (
	sftpExtensions = []string{"posix-rename@openssh.com"}
)

// Configuration for the SFTP server
type Configuration struct {
	// Identification string used by the server
	Banner string `json:"banner" mapstructure:"banner"`
	// The port used for serving SFTP requests
	BindPort int `json:"bind_port" mapstructure:"bind_port"`
	// The address to listen on. A blank value means listen on all available network interfaces.
	BindAddress string `json:"bind_address" mapstructure:"bind_address"`
	// Maximum idle timeout as minutes. If a client is idle for a time that exceeds this setting it will be disconnected.
	// 0 means disabled
	IdleTimeout int `json:"idle_timeout" mapstructure:"idle_timeout"`
	// Maximum number of authentication attempts permitted per connection.
	// If set to a negative number, the number of attempts is unlimited.
	// If set to zero, the number of attempts are limited to 6.
	MaxAuthTries int `json:"max_auth_tries" mapstructure:"max_auth_tries"`
	// Umask for new files
	Umask string `json:"umask" mapstructure:"umask"`
	// UploadMode 0 means standard, the files are uploaded directly to the requested path.
	// 1 means atomic: the files are uploaded to a temporary path and renamed to the requested path
	// when the client ends the upload. Atomic mode avoid problems such as a web server that
	// serves partial files when the files are being uploaded.
	// In atomic mode if there is an upload error the temporary file is deleted and so the requested
	// upload path will not contain a partial file.
	// 2 means atomic with resume support: as atomic but if there is an upload error the temporary
	// file is renamed to the requested path and not deleted, this way a client can reconnect and resume
	// the upload.
	UploadMode int `json:"upload_mode" mapstructure:"upload_mode"`
	// Actions to execute on SFTP create, download, delete and rename
	Actions Actions `json:"actions" mapstructure:"actions"`
	// Deprecated: please use HostKeys
	Keys []Key `json:"keys" mapstructure:"keys"`
	// HostKeys define the daemon's private host keys.
	// Each host key can be defined as a path relative to the configuration directory or an absolute one.
	// If empty or missing, the daemon will search or try to generate "id_rsa" and "id_ecdsa" host keys
	// inside the configuration directory.
	HostKeys []string `json:"host_keys" mapstructure:"host_keys"`
	// KexAlgorithms specifies the available KEX (Key Exchange) algorithms in
	// preference order.
	KexAlgorithms []string `json:"kex_algorithms" mapstructure:"kex_algorithms"`
	// Ciphers specifies the ciphers allowed
	Ciphers []string `json:"ciphers" mapstructure:"ciphers"`
	// MACs Specifies the available MAC (message authentication code) algorithms
	// in preference order
	MACs []string `json:"macs" mapstructure:"macs"`
	// TrustedUserCAKeys specifies a list of public keys paths of certificate authorities
	// that are trusted to sign user certificates for authentication.
	// The paths can be absolute or relative to the configuration directory
	TrustedUserCAKeys []string `json:"trusted_user_ca_keys" mapstructure:"trusted_user_ca_keys"`
	// LoginBannerFile the contents of the specified file, if any, are sent to
	// the remote user before authentication is allowed.
	LoginBannerFile string `json:"login_banner_file" mapstructure:"login_banner_file"`
	// SetstatMode 0 means "normal mode": requests for changing permissions and owner/group are executed.
	// 1 means "ignore mode": requests for changing permissions and owner/group are silently ignored.
	SetstatMode int `json:"setstat_mode" mapstructure:"setstat_mode"`
	// List of enabled SSH commands.
	// We support the following SSH commands:
	// - "scp". SCP is an experimental feature, we have our own SCP implementation since
	//      we can't rely on scp system command to proper handle permissions, quota and
	//      user's home dir restrictions.
	// 		The SCP protocol is quite simple but there is no official docs about it,
	// 		so we need more testing and feedbacks before enabling it by default.
	// 		We may not handle some borderline cases or have sneaky bugs.
	// 		Please do accurate tests yourself before enabling SCP and let us known
	// 		if something does not work as expected for your use cases.
	//      SCP between two remote hosts is supported using the `-3` scp option.
	// - "md5sum", "sha1sum", "sha256sum", "sha384sum", "sha512sum". Useful to check message
	//      digests for uploaded files. These commands are implemented inside SFTPGo so they
	//      work even if the matching system commands are not available, for example on Windows.
	// - "cd", "pwd". Some mobile SFTP clients does not support the SFTP SSH_FXP_REALPATH and so
	//      they use "cd" and "pwd" SSH commands to get the initial directory.
	//      Currently `cd` do nothing and `pwd` always returns the "/" path.
	//
	// The following SSH commands are enabled by default: "md5sum", "sha1sum", "cd", "pwd".
	// "*" enables all supported SSH commands.
	EnabledSSHCommands []string `json:"enabled_ssh_commands" mapstructure:"enabled_ssh_commands"`
	// Deprecated: please use KeyboardInteractiveHook
	KeyboardInteractiveProgram string `json:"keyboard_interactive_auth_program" mapstructure:"keyboard_interactive_auth_program"`
	// Absolute path to an external program or an HTTP URL to invoke for keyboard interactive authentication.
	// Leave empty to disable this authentication mode.
	KeyboardInteractiveHook string `json:"keyboard_interactive_auth_hook" mapstructure:"keyboard_interactive_auth_hook"`
	// Support for HAProxy PROXY protocol.
	// If you are running SFTPGo behind a proxy server such as HAProxy, AWS ELB or NGNIX, you can enable
	// the proxy protocol. It provides a convenient way to safely transport connection information
	// such as a client's address across multiple layers of NAT or TCP proxies to get the real
	// client IP address instead of the proxy IP. Both protocol versions 1 and 2 are supported.
	// - 0 means disabled
	// - 1 means proxy protocol enabled. Proxy header will be used and requests without proxy header will be accepted.
	// - 2 means proxy protocol required. Proxy header will be used and requests without proxy header will be rejected.
	// If the proxy protocol is enabled in SFTPGo then you have to enable the protocol in your proxy configuration too,
	// for example for HAProxy add "send-proxy" or "send-proxy-v2" to each server configuration line.
	ProxyProtocol int `json:"proxy_protocol" mapstructure:"proxy_protocol"`
	// List of IP addresses and IP ranges allowed to send the proxy header.
	// If proxy protocol is set to 1 and we receive a proxy header from an IP that is not in the list then the
	// connection will be accepted and the header will be ignored.
	// If proxy protocol is set to 2 and we receive a proxy header from an IP that is not in the list then the
	// connection will be rejected.
	ProxyAllowed     []string `json:"proxy_allowed" mapstructure:"proxy_allowed"`
	certChecker      *ssh.CertChecker
	parsedUserCAKeys []ssh.PublicKey
}

// Key contains information about host keys
// Deprecated: please use HostKeys
type Key struct {
	// The private key path as absolute path or relative to the configuration directory
	PrivateKey string `json:"private_key" mapstructure:"private_key"`
}

type authenticationError struct {
	err string
}

func (e *authenticationError) Error() string {
	return fmt.Sprintf("Authentication error: %s", e.err)
}

// Initialize the SFTP server and add a persistent listener to handle inbound SFTP connections.
func (c Configuration) Initialize(configDir string) error {
	umask, err := strconv.ParseUint(c.Umask, 8, 8)
	if err == nil {
		utils.SetUmask(int(umask), c.Umask)
	} else {
		logger.Warn(logSender, "", "error reading umask, please fix your config file: %v", err)
		logger.WarnToConsole("error reading umask, please fix your config file: %v", err)
	}
	serverConfig := &ssh.ServerConfig{
		NoClientAuth: false,
		MaxAuthTries: c.MaxAuthTries,
		PasswordCallback: func(conn ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			sp, err := c.validatePasswordCredentials(conn, pass)
			if err != nil {
				return nil, &authenticationError{err: fmt.Sprintf("could not validate password credentials: %v", err)}
			}

			return sp, nil
		},
		PublicKeyCallback: func(conn ssh.ConnMetadata, pubKey ssh.PublicKey) (*ssh.Permissions, error) {
			sp, err := c.validatePublicKeyCredentials(conn, pubKey)
			if err == ssh.ErrPartialSuccess {
				return sp, err
			}
			if err != nil {
				return nil, &authenticationError{err: fmt.Sprintf("could not validate public key credentials: %v", err)}
			}

			return sp, nil
		},
		NextAuthMethodsCallback: func(conn ssh.ConnMetadata) []string {
			var nextMethods []string
			user, err := dataprovider.UserExists(conn.User())
			if err == nil {
				nextMethods = user.GetNextAuthMethods(conn.PartialSuccessMethods())
			}
			return nextMethods
		},
		ServerVersion: fmt.Sprintf("SSH-2.0-%v", c.Banner),
	}

	if err = c.checkAndLoadHostKeys(configDir, serverConfig); err != nil {
		return err
	}

	if err = c.initializeCertChecker(configDir); err != nil {
		return err
	}

	sftp.SetSFTPExtensions(sftpExtensions...) //nolint:errcheck // we configure valid SFTP Extensions so we cannot get an error

	c.configureSecurityOptions(serverConfig)
	c.configureKeyboardInteractiveAuth(serverConfig)
	c.configureLoginBanner(serverConfig, configDir)
	c.checkSSHCommands()

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", c.BindAddress, c.BindPort))
	if err != nil {
		logger.Warn(logSender, "", "error starting listener on address %s:%d: %v", c.BindAddress, c.BindPort, err)
		return err
	}
	proxyListener, err := c.getProxyListener(listener)
	if err != nil {
		logger.Warn(logSender, "", "error enabling proxy listener: %v", err)
		return err
	}
	actions = c.Actions
	uploadMode = c.UploadMode
	setstatMode = c.SetstatMode
	logger.Info(logSender, "", "server listener registered address: %v", listener.Addr().String())
	c.checkIdleTimer()

	for {
		var conn net.Conn
		if proxyListener != nil {
			conn, err = proxyListener.Accept()
		} else {
			conn, err = listener.Accept()
		}
		if conn != nil && err == nil {
			go c.AcceptInboundConnection(conn, serverConfig)
		}
	}
}

func (c *Configuration) getProxyListener(listener net.Listener) (*proxyproto.Listener, error) {
	var proxyListener *proxyproto.Listener
	var err error
	if c.ProxyProtocol > 0 {
		var policyFunc func(upstream net.Addr) (proxyproto.Policy, error)
		if c.ProxyProtocol == 1 && len(c.ProxyAllowed) > 0 {
			policyFunc, err = proxyproto.LaxWhiteListPolicy(c.ProxyAllowed)
			if err != nil {
				return nil, err
			}
		}
		if c.ProxyProtocol == 2 {
			if len(c.ProxyAllowed) == 0 {
				policyFunc = func(upstream net.Addr) (proxyproto.Policy, error) {
					return proxyproto.REQUIRE, nil
				}
			} else {
				policyFunc, err = proxyproto.StrictWhiteListPolicy(c.ProxyAllowed)
				if err != nil {
					return nil, err
				}
			}
		}
		proxyListener = &proxyproto.Listener{
			Listener: listener,
			Policy:   policyFunc,
		}
	}
	return proxyListener, nil
}

func (c Configuration) checkIdleTimer() {
	if c.IdleTimeout > 0 {
		startIdleTimer(time.Duration(c.IdleTimeout) * time.Minute)
	}
}

func (c Configuration) configureSecurityOptions(serverConfig *ssh.ServerConfig) {
	if len(c.KexAlgorithms) > 0 {
		serverConfig.KeyExchanges = c.KexAlgorithms
	}
	if len(c.Ciphers) > 0 {
		serverConfig.Ciphers = c.Ciphers
	}
	if len(c.MACs) > 0 {
		serverConfig.MACs = c.MACs
	}
}

func (c Configuration) configureLoginBanner(serverConfig *ssh.ServerConfig, configDir string) {
	if len(c.LoginBannerFile) > 0 {
		bannerFilePath := c.LoginBannerFile
		if !filepath.IsAbs(bannerFilePath) {
			bannerFilePath = filepath.Join(configDir, bannerFilePath)
		}
		bannerContent, err := ioutil.ReadFile(bannerFilePath)
		if err == nil {
			banner := string(bannerContent)
			serverConfig.BannerCallback = func(conn ssh.ConnMetadata) string {
				return banner
			}
		} else {
			logger.WarnToConsole("unable to read login banner file: %v", err)
			logger.Warn(logSender, "", "unable to read login banner file: %v", err)
		}
	}
}

func (c Configuration) configureKeyboardInteractiveAuth(serverConfig *ssh.ServerConfig) {
	if len(c.KeyboardInteractiveHook) == 0 {
		return
	}
	if !strings.HasPrefix(c.KeyboardInteractiveHook, "http") {
		if !filepath.IsAbs(c.KeyboardInteractiveHook) {
			logger.WarnToConsole("invalid keyboard interactive authentication program: %#v must be an absolute path",
				c.KeyboardInteractiveHook)
			logger.Warn(logSender, "", "invalid keyboard interactive authentication program: %#v must be an absolute path",
				c.KeyboardInteractiveHook)
			return
		}
		_, err := os.Stat(c.KeyboardInteractiveHook)
		if err != nil {
			logger.WarnToConsole("invalid keyboard interactive authentication program:: %v", err)
			logger.Warn(logSender, "", "invalid keyboard interactive authentication program:: %v", err)
			return
		}
	}
	serverConfig.KeyboardInteractiveCallback = func(conn ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		sp, err := c.validateKeyboardInteractiveCredentials(conn, client)
		if err != nil {
			return nil, &authenticationError{err: fmt.Sprintf("could not validate keyboard interactive credentials: %v", err)}
		}

		return sp, nil
	}
}

// AcceptInboundConnection handles an inbound connection to the server instance and determines if the request should be served or not.
func (c Configuration) AcceptInboundConnection(conn net.Conn, config *ssh.ServerConfig) {
	// Before beginning a handshake must be performed on the incoming net.Conn
	// we'll set a Deadline for handshake to complete, the default is 2 minutes as OpenSSH
	conn.SetDeadline(time.Now().Add(handshakeTimeout)) //nolint:errcheck
	remoteAddr := conn.RemoteAddr()
	sconn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		logger.Warn(logSender, "", "failed to accept an incoming connection: %v", err)
		if _, ok := err.(*ssh.ServerAuthError); !ok {
			logger.ConnectionFailedLog("", utils.GetIPFromRemoteAddress(remoteAddr.String()), "no_auth_tryed", err.Error())
		}
		return
	}
	// handshake completed so remove the deadline, we'll use IdleTimeout configuration from now on
	conn.SetDeadline(time.Time{}) //nolint:errcheck

	var user dataprovider.User

	// Unmarshal cannot fails here and even if it fails we'll have a user with no permissions
	json.Unmarshal([]byte(sconn.Permissions.Extensions["sftpgo_user"]), &user) //nolint:errcheck

	loginType := sconn.Permissions.Extensions["sftpgo_login_method"]
	connectionID := hex.EncodeToString(sconn.SessionID())

	fs, err := user.GetFilesystem(connectionID)

	if err != nil {
		logger.Warn(logSender, "", "could create filesystem for user %#v err: %v", user.Username, err)
		conn.Close()
		return
	}

	connection := Connection{
		ID:            connectionID,
		User:          user,
		ClientVersion: string(sconn.ClientVersion()),
		RemoteAddr:    remoteAddr,
		StartTime:     time.Now(),
		lastActivity:  time.Now(),
		netConn:       conn,
		channel:       nil,
		fs:            fs,
	}

	connection.fs.CheckRootPath(user.Username, user.GetUID(), user.GetGID())

	connection.Log(logger.LevelInfo, logSender, "User id: %d, logged in with: %#v, username: %#v, home_dir: %#v remote addr: %#v",
		user.ID, loginType, user.Username, user.HomeDir, remoteAddr.String())
	dataprovider.UpdateLastLogin(user) //nolint:errcheck

	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		// If its not a session channel we just move on because its not something we
		// know how to handle at this point.
		if newChannel.ChannelType() != "session" {
			connection.Log(logger.LevelDebug, logSender, "received an unknown channel type: %v", newChannel.ChannelType())
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type") //nolint:errcheck
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			connection.Log(logger.LevelWarn, logSender, "could not accept a channel: %v", err)
			continue
		}

		// Channels have a type that is dependent on the protocol. For SFTP this is "subsystem"
		// with a payload that (should) be "sftp". Discard anything else we receive ("pty", "shell", etc)
		go func(in <-chan *ssh.Request) {
			for req := range in {
				ok := false

				switch req.Type {
				case "subsystem":
					if string(req.Payload[4:]) == "sftp" {
						ok = true
						connection.protocol = protocolSFTP
						connection.channel = channel
						go c.handleSftpConnection(channel, connection)
					}
				case "exec":
					ok = processSSHCommand(req.Payload, &connection, channel, c.EnabledSSHCommands)
				}
				req.Reply(ok, nil) //nolint:errcheck
			}
		}(requests)
	}
}

func (c Configuration) handleSftpConnection(channel ssh.Channel, connection Connection) {
	addConnection(connection)
	defer removeConnection(connection)
	// Create a new handler for the currently logged in user's server.
	handler := c.createHandler(connection)

	// Create the server instance for the channel using the handler we created above.
	server := sftp.NewRequestServer(channel, handler, sftp.WithRSAllocator())

	if err := server.Serve(); err == io.EOF {
		connection.Log(logger.LevelDebug, logSender, "connection closed, sending exit status")
		exitStatus := sshSubsystemExitStatus{Status: uint32(0)}
		_, err = channel.SendRequest("exit-status", false, ssh.Marshal(&exitStatus))
		connection.Log(logger.LevelDebug, logSender, "sent exit status %+v error: %v", exitStatus, err)
		server.Close()
	} else if err != nil {
		connection.Log(logger.LevelWarn, logSender, "connection closed with error: %v", err)
	}
}

func (c Configuration) createHandler(connection Connection) sftp.Handlers {
	return sftp.Handlers{
		FileGet:  connection,
		FilePut:  connection,
		FileCmd:  connection,
		FileList: connection,
	}
}

func loginUser(user dataprovider.User, loginMethod, publicKey string, conn ssh.ConnMetadata) (*ssh.Permissions, error) {
	connectionID := ""
	if conn != nil {
		connectionID = hex.EncodeToString(conn.SessionID())
	}
	if !filepath.IsAbs(user.HomeDir) {
		logger.Warn(logSender, connectionID, "user %#v has an invalid home dir: %#v. Home dir must be an absolute path, login not allowed",
			user.Username, user.HomeDir)
		return nil, fmt.Errorf("cannot login user with invalid home dir: %#v", user.HomeDir)
	}
	if user.MaxSessions > 0 {
		activeSessions := getActiveSessions(user.Username)
		if activeSessions >= user.MaxSessions {
			logger.Debug(logSender, "", "authentication refused for user: %#v, too many open sessions: %v/%v", user.Username,
				activeSessions, user.MaxSessions)
			return nil, fmt.Errorf("too many open sessions: %v", activeSessions)
		}
	}
	if !user.IsLoginMethodAllowed(loginMethod, conn.PartialSuccessMethods()) {
		logger.Debug(logSender, connectionID, "cannot login user %#v, login method %#v is not allowed", user.Username, loginMethod)
		return nil, fmt.Errorf("Login method %#v is not allowed for user %#v", loginMethod, user.Username)
	}
	if dataprovider.GetQuotaTracking() > 0 && user.HasOverlappedMappedPaths() {
		logger.Debug(logSender, connectionID, "cannot login user %#v, overlapping mapped folders are allowed only with quota tracking disabled",
			user.Username)
		return nil, errors.New("overlapping mapped folders are allowed only with quota tracking disabled")
	}
	remoteAddr := conn.RemoteAddr().String()
	if !user.IsLoginFromAddrAllowed(remoteAddr) {
		logger.Debug(logSender, connectionID, "cannot login user %#v, remote address is not allowed: %v", user.Username, remoteAddr)
		return nil, fmt.Errorf("Login for user %#v is not allowed from this address: %v", user.Username, remoteAddr)
	}

	json, err := json.Marshal(user)
	if err != nil {
		logger.Warn(logSender, connectionID, "error serializing user info: %v, authentication rejected", err)
		return nil, err
	}
	if len(publicKey) > 0 {
		loginMethod = fmt.Sprintf("%v: %v", loginMethod, publicKey)
	}
	p := &ssh.Permissions{}
	p.Extensions = make(map[string]string)
	p.Extensions["sftpgo_user"] = string(json)
	p.Extensions["sftpgo_login_method"] = loginMethod
	return p, nil
}

func (c *Configuration) checkSSHCommands() {
	if utils.IsStringInSlice("*", c.EnabledSSHCommands) {
		c.EnabledSSHCommands = GetSupportedSSHCommands()
		return
	}
	sshCommands := []string{}
	for _, command := range c.EnabledSSHCommands {
		if utils.IsStringInSlice(command, supportedSSHCommands) {
			sshCommands = append(sshCommands, command)
		} else {
			logger.Warn(logSender, "", "unsupported ssh command: %#v ignored", command)
			logger.WarnToConsole("unsupported ssh command: %#v ignored", command)
		}
	}
	c.EnabledSSHCommands = sshCommands
}

func (c *Configuration) checkHostKeyAutoGeneration(configDir string) error {
	for _, k := range c.HostKeys {
		if filepath.IsAbs(k) {
			if _, err := os.Stat(k); os.IsNotExist(err) {
				keyName := filepath.Base(k)
				switch keyName {
				case defaultPrivateRSAKeyName:
					logger.Info(logSender, "", "try to create non-existent host key %#v", k)
					logger.InfoToConsole("try to create non-existent host key %#v", k)
					err = utils.GenerateRSAKeys(k)
					if err != nil {
						return err
					}
				case defaultPrivateECDSAKeyName:
					logger.Info(logSender, "", "try to create non-existent host key %#v", k)
					logger.InfoToConsole("try to create non-existent host key %#v", k)
					err = utils.GenerateECDSAKeys(k)
					if err != nil {
						return err
					}
				default:
					logger.Warn(logSender, "", "non-existent host key %#v will not be created", k)
					logger.WarnToConsole("non-existent host key %#v will not be created", k)
				}
			}
		}
	}
	if len(c.HostKeys) == 0 {
		defaultKeys := []string{defaultPrivateRSAKeyName, defaultPrivateECDSAKeyName}
		for _, k := range defaultKeys {
			autoFile := filepath.Join(configDir, k)
			if _, err := os.Stat(autoFile); os.IsNotExist(err) {
				logger.Info(logSender, "", "No host keys configured and %#v does not exist; try to create a new host key", autoFile)
				logger.InfoToConsole("No host keys configured and %#v does not exist; try to create a new host key", autoFile)
				if k == defaultPrivateRSAKeyName {
					err = utils.GenerateRSAKeys(autoFile)
				} else {
					err = utils.GenerateECDSAKeys(autoFile)
				}
				if err != nil {
					return err
				}
			}
			c.HostKeys = append(c.HostKeys, k)
		}
	}
	return nil
}

// If no host keys are defined we try to use or generate the default ones.
func (c *Configuration) checkAndLoadHostKeys(configDir string, serverConfig *ssh.ServerConfig) error {
	if err := c.checkHostKeyAutoGeneration(configDir); err != nil {
		return err
	}
	for _, k := range c.HostKeys {
		hostKey := k
		if !utils.IsFileInputValid(hostKey) {
			logger.Warn(logSender, "", "unable to load invalid host key: %#v", hostKey)
			logger.WarnToConsole("unable to load invalid host key: %#v", hostKey)
			continue
		}
		if !filepath.IsAbs(hostKey) {
			hostKey = filepath.Join(configDir, hostKey)
		}
		logger.Info(logSender, "", "Loading private host key: %s", hostKey)

		privateBytes, err := ioutil.ReadFile(hostKey)
		if err != nil {
			return err
		}

		private, err := ssh.ParsePrivateKey(privateBytes)
		if err != nil {
			return err
		}

		// Add private key to the server configuration.
		serverConfig.AddHostKey(private)
	}
	return nil
}

func (c *Configuration) initializeCertChecker(configDir string) error {
	for _, keyPath := range c.TrustedUserCAKeys {
		if !utils.IsFileInputValid(keyPath) {
			logger.Warn(logSender, "", "unable to load invalid trusted user CA key: %#v", keyPath)
			logger.WarnToConsole("unable to load invalid trusted user CA key: %#v", keyPath)
			continue
		}
		if !filepath.IsAbs(keyPath) {
			keyPath = filepath.Join(configDir, keyPath)
		}
		keyBytes, err := ioutil.ReadFile(keyPath)
		if err != nil {
			logger.Warn(logSender, "", "error loading trusted user CA key %#v: %v", keyPath, err)
			logger.WarnToConsole("error loading trusted user CA key %#v: %v", keyPath, err)
			return err
		}
		parsedKey, _, _, _, err := ssh.ParseAuthorizedKey(keyBytes)
		if err != nil {
			logger.Warn(logSender, "", "error parsing trusted user CA key %#v: %v", keyPath, err)
			logger.WarnToConsole("error parsing trusted user CA key %#v: %v", keyPath, err)
			return err
		}
		c.parsedUserCAKeys = append(c.parsedUserCAKeys, parsedKey)
	}
	c.certChecker = &ssh.CertChecker{
		SupportedCriticalOptions: []string{
			sourceAddressCriticalOption,
		},
		IsUserAuthority: func(k ssh.PublicKey) bool {
			for _, key := range c.parsedUserCAKeys {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					return true
				}
			}
			return false
		},
	}
	return nil
}

func (c Configuration) validatePublicKeyCredentials(conn ssh.ConnMetadata, pubKey ssh.PublicKey) (*ssh.Permissions, error) {
	var err error
	var user dataprovider.User
	var keyID string
	var sshPerm *ssh.Permissions
	var certPerm *ssh.Permissions

	connectionID := hex.EncodeToString(conn.SessionID())
	method := dataprovider.SSHLoginMethodPublicKey
	cert, ok := pubKey.(*ssh.Certificate)
	if ok {
		if cert.CertType != ssh.UserCert {
			err = fmt.Errorf("ssh: cert has type %d", cert.CertType)
			updateLoginMetrics(conn, method, err)
			return nil, err
		}
		if !c.certChecker.IsUserAuthority(cert.SignatureKey) {
			err = fmt.Errorf("ssh: certificate signed by unrecognized authority")
			updateLoginMetrics(conn, method, err)
			return nil, err
		}
		if err := c.certChecker.CheckCert(conn.User(), cert); err != nil {
			updateLoginMetrics(conn, method, err)
			return nil, err
		}
		certPerm = &cert.Permissions
	}
	if user, keyID, err = dataprovider.CheckUserAndPubKey(conn.User(), pubKey.Marshal()); err == nil {
		if user.IsPartialAuth(method) {
			logger.Debug(logSender, connectionID, "user %#v authenticated with partial success", conn.User())
			return certPerm, ssh.ErrPartialSuccess
		}
		sshPerm, err = loginUser(user, method, keyID, conn)
		if err == nil && certPerm != nil {
			// if we have a SSH user cert we need to merge certificate permissions with our ones
			// we only set Extensions, so CriticalOptions are always the ones from the certificate
			sshPerm.CriticalOptions = certPerm.CriticalOptions
			if certPerm.Extensions != nil {
				for k, v := range certPerm.Extensions {
					sshPerm.Extensions[k] = v
				}
			}
		}
	}
	updateLoginMetrics(conn, method, err)
	return sshPerm, err
}

func (c Configuration) validatePasswordCredentials(conn ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
	var err error
	var user dataprovider.User
	var sshPerm *ssh.Permissions

	method := dataprovider.SSHLoginMethodPassword
	if len(conn.PartialSuccessMethods()) == 1 {
		method = dataprovider.SSHLoginMethodKeyAndPassword
	}
	if user, err = dataprovider.CheckUserAndPass(conn.User(), string(pass)); err == nil {
		sshPerm, err = loginUser(user, method, "", conn)
	}
	updateLoginMetrics(conn, method, err)
	return sshPerm, err
}

func (c Configuration) validateKeyboardInteractiveCredentials(conn ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
	var err error
	var user dataprovider.User
	var sshPerm *ssh.Permissions

	method := dataprovider.SSHLoginMethodKeyboardInteractive
	if len(conn.PartialSuccessMethods()) == 1 {
		method = dataprovider.SSHLoginMethodKeyAndKeyboardInt
	}
	if user, err = dataprovider.CheckKeyboardInteractiveAuth(conn.User(), c.KeyboardInteractiveHook, client); err == nil {
		sshPerm, err = loginUser(user, method, "", conn)
	}
	updateLoginMetrics(conn, method, err)
	return sshPerm, err
}

func updateLoginMetrics(conn ssh.ConnMetadata, method string, err error) {
	metrics.AddLoginAttempt(method)
	if err != nil {
		logger.ConnectionFailedLog(conn.User(), utils.GetIPFromRemoteAddress(conn.RemoteAddr().String()), method, err.Error())
	}
	metrics.AddLoginResult(method, err)
}
