// Package sftpd implements the SSH File Transfer Protocol as described in https://tools.ietf.org/html/draft-ietf-secsh-filexfer-02.
// It uses pkg/sftp library:
// https://github.com/pkg/sftp
package sftpd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/drakkan/sftpgo/dataprovider"
	"github.com/drakkan/sftpgo/httpclient"
	"github.com/drakkan/sftpgo/logger"
	"github.com/drakkan/sftpgo/metrics"
	"github.com/drakkan/sftpgo/utils"
)

const (
	logSender           = "sftpd"
	logSenderSCP        = "scp"
	logSenderSSH        = "ssh"
	uploadLogSender     = "Upload"
	downloadLogSender   = "Download"
	renameLogSender     = "Rename"
	rmdirLogSender      = "Rmdir"
	mkdirLogSender      = "Mkdir"
	symlinkLogSender    = "Symlink"
	removeLogSender     = "Remove"
	chownLogSender      = "Chown"
	chmodLogSender      = "Chmod"
	chtimesLogSender    = "Chtimes"
	sshCommandLogSender = "SSHCommand"
	operationDownload   = "download"
	operationUpload     = "upload"
	operationDelete     = "delete"
	operationPreDelete  = "pre-delete"
	operationRename     = "rename"
	operationSSHCmd     = "ssh_cmd"
	protocolSFTP        = "SFTP"
	protocolSCP         = "SCP"
	protocolSSH         = "SSH"
	handshakeTimeout    = 2 * time.Minute
)

const (
	uploadModeStandard = iota
	uploadModeAtomic
	uploadModeAtomicWithResume
)

var (
	mutex                   sync.RWMutex
	openConnections         map[string]Connection
	activeTransfers         []*Transfer
	idleTimeout             time.Duration
	activeQuotaScans        []ActiveQuotaScan
	activeVFoldersQuotaScan []ActiveVirtualFolderQuotaScan
	actions                 Actions
	uploadMode              int
	setstatMode             int
	supportedSSHCommands    = []string{"scp", "md5sum", "sha1sum", "sha256sum", "sha384sum", "sha512sum", "cd", "pwd",
		"git-receive-pack", "git-upload-pack", "git-upload-archive", "rsync", "sftpgo-copy", "sftpgo-remove"}
	defaultSSHCommands       = []string{"md5sum", "sha1sum", "cd", "pwd", "scp"}
	sshHashCommands          = []string{"md5sum", "sha1sum", "sha256sum", "sha384sum", "sha512sum"}
	systemCommands           = []string{"git-receive-pack", "git-upload-pack", "git-upload-archive", "rsync"}
	errUnconfiguredAction    = errors.New("no hook is configured for this action")
	errNoHook                = errors.New("unable to execute action, no hook defined")
	errUnexpectedHTTResponse = errors.New("unexpected HTTP response code")
)

type connectionTransfer struct {
	OperationType string `json:"operation_type"`
	StartTime     int64  `json:"start_time"`
	Size          int64  `json:"size"`
	LastActivity  int64  `json:"last_activity"`
	Path          string `json:"path"`
}

// ActiveQuotaScan defines an active quota scan for a user home dir
type ActiveQuotaScan struct {
	// Username to which the quota scan refers
	Username string `json:"username"`
	// quota scan start time as unix timestamp in milliseconds
	StartTime int64 `json:"start_time"`
}

// ActiveVirtualFolderQuotaScan defines an active quota scan for a virtual folder
type ActiveVirtualFolderQuotaScan struct {
	// folder path to which the quota scan refers
	MappedPath string `json:"mapped_path"`
	// quota scan start time as unix timestamp in milliseconds
	StartTime int64 `json:"start_time"`
}

// Actions to execute on SFTP create, download, delete and rename.
// An external command can be executed and/or an HTTP notification can be fired
type Actions struct {
	// Valid values are download, upload, delete, rename, ssh_cmd. Empty slice to disable
	ExecuteOn []string `json:"execute_on" mapstructure:"execute_on"`
	// Deprecated: please use Hook
	Command string `json:"command" mapstructure:"command"`
	// Deprecated: please use Hook
	HTTPNotificationURL string `json:"http_notification_url" mapstructure:"http_notification_url"`
	// Absolute path to an external program or an HTTP URL
	Hook string `json:"hook" mapstructure:"hook"`
}

// ConnectionStatus status for an active connection
type ConnectionStatus struct {
	// Logged in username
	Username string `json:"username"`
	// Unique identifier for the connection
	ConnectionID string `json:"connection_id"`
	// client's version string
	ClientVersion string `json:"client_version"`
	// Remote address for this connection
	RemoteAddress string `json:"remote_address"`
	// Connection time as unix timestamp in milliseconds
	ConnectionTime int64 `json:"connection_time"`
	// Last activity as unix timestamp in milliseconds
	LastActivity int64 `json:"last_activity"`
	// Protocol for this connection: SFTP, SCP, SSH
	Protocol string `json:"protocol"`
	// active uploads/downloads
	Transfers []connectionTransfer `json:"active_transfers"`
	// for protocol SSH this is the issued command
	SSHCommand string `json:"ssh_command"`
}

type sshSubsystemExitStatus struct {
	Status uint32
}

type sshSubsystemExecMsg struct {
	Command string
}

type actionNotification struct {
	Action     string `json:"action"`
	Username   string `json:"username"`
	Path       string `json:"path"`
	TargetPath string `json:"target_path,omitempty"`
	SSHCmd     string `json:"ssh_cmd,omitempty"`
	FileSize   int64  `json:"file_size,omitempty"`
	FsProvider int    `json:"fs_provider"`
	Bucket     string `json:"bucket,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Status     int    `json:"status"`
}

func newActionNotification(user dataprovider.User, operation, filePath, target, sshCmd string, fileSize int64,
	err error) actionNotification {
	bucket := ""
	endpoint := ""
	status := 1
	if user.FsConfig.Provider == 1 {
		bucket = user.FsConfig.S3Config.Bucket
		endpoint = user.FsConfig.S3Config.Endpoint
	} else if user.FsConfig.Provider == 2 {
		bucket = user.FsConfig.GCSConfig.Bucket
	}
	if err == errQuotaExceeded {
		status = 2
	} else if err != nil {
		status = 0
	}
	return actionNotification{
		Action:     operation,
		Username:   user.Username,
		Path:       filePath,
		TargetPath: target,
		SSHCmd:     sshCmd,
		FileSize:   fileSize,
		FsProvider: user.FsConfig.Provider,
		Bucket:     bucket,
		Endpoint:   endpoint,
		Status:     status,
	}
}

func (a *actionNotification) AsJSON() []byte {
	res, _ := json.Marshal(a)
	return res
}

func (a *actionNotification) AsEnvVars() []string {
	return []string{fmt.Sprintf("SFTPGO_ACTION=%v", a.Action),
		fmt.Sprintf("SFTPGO_ACTION_USERNAME=%v", a.Username),
		fmt.Sprintf("SFTPGO_ACTION_PATH=%v", a.Path),
		fmt.Sprintf("SFTPGO_ACTION_TARGET=%v", a.TargetPath),
		fmt.Sprintf("SFTPGO_ACTION_SSH_CMD=%v", a.SSHCmd),
		fmt.Sprintf("SFTPGO_ACTION_FILE_SIZE=%v", a.FileSize),
		fmt.Sprintf("SFTPGO_ACTION_FS_PROVIDER=%v", a.FsProvider),
		fmt.Sprintf("SFTPGO_ACTION_BUCKET=%v", a.Bucket),
		fmt.Sprintf("SFTPGO_ACTION_ENDPOINT=%v", a.Endpoint),
		fmt.Sprintf("SFTPGO_ACTION_STATUS=%v", a.Status),
	}
}

func init() {
	openConnections = make(map[string]Connection)
}

// GetDefaultSSHCommands returns the SSH commands enabled as default
func GetDefaultSSHCommands() []string {
	result := make([]string, len(defaultSSHCommands))
	copy(result, defaultSSHCommands)
	return result
}

// GetSupportedSSHCommands returns the supported SSH commands
func GetSupportedSSHCommands() []string {
	result := make([]string, len(supportedSSHCommands))
	copy(result, supportedSSHCommands)
	return result
}

// GetConnectionDuration returns the connection duration as string
func (c ConnectionStatus) GetConnectionDuration() string {
	elapsed := time.Since(utils.GetTimeFromMsecSinceEpoch(c.ConnectionTime))
	return utils.GetDurationAsString(elapsed)
}

// GetConnectionInfo returns connection info.
// Protocol,Client Version and RemoteAddress are returned.
// For SSH commands the issued command is returned too.
func (c ConnectionStatus) GetConnectionInfo() string {
	result := fmt.Sprintf("%v. Client: %#v From: %#v", c.Protocol, c.ClientVersion, c.RemoteAddress)
	if c.Protocol == protocolSSH && len(c.SSHCommand) > 0 {
		result += fmt.Sprintf(". Command: %#v", c.SSHCommand)
	}
	return result
}

// GetTransfersAsString returns the active transfers as string
func (c ConnectionStatus) GetTransfersAsString() string {
	result := ""
	for _, t := range c.Transfers {
		if len(result) > 0 {
			result += ". "
		}
		result += t.getConnectionTransferAsString()
	}
	return result
}

func (t connectionTransfer) getConnectionTransferAsString() string {
	result := ""
	if t.OperationType == operationUpload {
		result += "UL"
	} else {
		result += "DL"
	}
	result += fmt.Sprintf(" %#v ", t.Path)
	if t.Size > 0 {
		elapsed := time.Since(utils.GetTimeFromMsecSinceEpoch(t.StartTime))
		speed := float64(t.Size) / float64(utils.GetTimeAsMsSinceEpoch(time.Now())-t.StartTime)
		result += fmt.Sprintf("Size: %#v Elapsed: %#v Speed: \"%.1f KB/s\"", utils.ByteCountSI(t.Size),
			utils.GetDurationAsString(elapsed), speed)
	}
	return result
}

func getActiveSessions(username string) int {
	mutex.RLock()
	defer mutex.RUnlock()
	numSessions := 0
	for _, c := range openConnections {
		if c.User.Username == username {
			numSessions++
		}
	}
	return numSessions
}

// GetQuotaScans returns the active quota scans for users home directories
func GetQuotaScans() []ActiveQuotaScan {
	mutex.RLock()
	defer mutex.RUnlock()
	scans := make([]ActiveQuotaScan, len(activeQuotaScans))
	copy(scans, activeQuotaScans)
	return scans
}

// AddQuotaScan add a user to the ones with active quota scans.
// Returns false if the user has a quota scan already running
func AddQuotaScan(username string) bool {
	mutex.Lock()
	defer mutex.Unlock()
	for _, s := range activeQuotaScans {
		if s.Username == username {
			return false
		}
	}
	activeQuotaScans = append(activeQuotaScans, ActiveQuotaScan{
		Username:  username,
		StartTime: utils.GetTimeAsMsSinceEpoch(time.Now()),
	})
	return true
}

// RemoveQuotaScan removes a user from the ones with active quota scans
func RemoveQuotaScan(username string) error {
	mutex.Lock()
	defer mutex.Unlock()
	var err error
	indexToRemove := -1
	for i, s := range activeQuotaScans {
		if s.Username == username {
			indexToRemove = i
			break
		}
	}
	if indexToRemove >= 0 {
		activeQuotaScans[indexToRemove] = activeQuotaScans[len(activeQuotaScans)-1]
		activeQuotaScans = activeQuotaScans[:len(activeQuotaScans)-1]
	} else {
		err = fmt.Errorf("quota scan to remove not found for user: %#v", username)
		logger.Warn(logSender, "", "error: %v", err)
	}
	return err
}

// GetVFoldersQuotaScans returns the active quota scans for virtual folders
func GetVFoldersQuotaScans() []ActiveVirtualFolderQuotaScan {
	mutex.RLock()
	defer mutex.RUnlock()
	scans := make([]ActiveVirtualFolderQuotaScan, len(activeVFoldersQuotaScan))
	copy(scans, activeVFoldersQuotaScan)
	return scans
}

// AddVFolderQuotaScan add a virtual folder to the ones with active quota scans.
// Returns false if the folder has a quota scan already running
func AddVFolderQuotaScan(folderPath string) bool {
	mutex.Lock()
	defer mutex.Unlock()
	for _, s := range activeVFoldersQuotaScan {
		if s.MappedPath == folderPath {
			return false
		}
	}
	activeVFoldersQuotaScan = append(activeVFoldersQuotaScan, ActiveVirtualFolderQuotaScan{
		MappedPath: folderPath,
		StartTime:  utils.GetTimeAsMsSinceEpoch(time.Now()),
	})
	return true
}

// RemoveVFolderQuotaScan removes a folder from the ones with active quota scans
func RemoveVFolderQuotaScan(folderPath string) error {
	mutex.Lock()
	defer mutex.Unlock()
	var err error
	indexToRemove := -1
	for i, s := range activeVFoldersQuotaScan {
		if s.MappedPath == folderPath {
			indexToRemove = i
			break
		}
	}
	if indexToRemove >= 0 {
		activeVFoldersQuotaScan[indexToRemove] = activeVFoldersQuotaScan[len(activeVFoldersQuotaScan)-1]
		activeVFoldersQuotaScan = activeVFoldersQuotaScan[:len(activeVFoldersQuotaScan)-1]
	} else {
		err = fmt.Errorf("quota scan to remove not found for user: %#v", folderPath)
		logger.Warn(logSender, "", "error: %v", err)
	}
	return err
}

// CloseActiveConnection closes an active SFTP connection.
// It returns true on success
func CloseActiveConnection(connectionID string) bool {
	result := false
	mutex.RLock()
	defer mutex.RUnlock()
	if c, ok := openConnections[connectionID]; ok {
		err := c.close()
		c.Log(logger.LevelDebug, logSender, "close connection requested, close err: %v", err)
		result = true
	}
	return result
}

// GetConnectionsStats returns stats for active connections
func GetConnectionsStats() []ConnectionStatus {
	mutex.RLock()
	defer mutex.RUnlock()
	stats := []ConnectionStatus{}
	for _, c := range openConnections {
		conn := ConnectionStatus{
			Username:       c.User.Username,
			ConnectionID:   c.ID,
			ClientVersion:  c.ClientVersion,
			RemoteAddress:  c.RemoteAddr.String(),
			ConnectionTime: utils.GetTimeAsMsSinceEpoch(c.StartTime),
			LastActivity:   utils.GetTimeAsMsSinceEpoch(c.lastActivity),
			Protocol:       c.protocol,
			Transfers:      []connectionTransfer{},
			SSHCommand:     c.command,
		}
		for _, t := range activeTransfers {
			if t.connectionID == c.ID {
				if t.lastActivity.UnixNano() > c.lastActivity.UnixNano() {
					conn.LastActivity = utils.GetTimeAsMsSinceEpoch(t.lastActivity)
				}
				var operationType string
				var size int64
				if t.transferType == transferUpload {
					operationType = operationUpload
					size = t.bytesReceived
				} else {
					operationType = operationDownload
					size = t.bytesSent
				}
				connTransfer := connectionTransfer{
					OperationType: operationType,
					StartTime:     utils.GetTimeAsMsSinceEpoch(t.start),
					Size:          size,
					LastActivity:  utils.GetTimeAsMsSinceEpoch(t.lastActivity),
					Path:          c.fs.GetRelativePath(t.path),
				}
				conn.Transfers = append(conn.Transfers, connTransfer)
			}
		}
		stats = append(stats, conn)
	}
	return stats
}

func startIdleTimer(maxIdleTime time.Duration) {
	idleTimeout = maxIdleTime
	go func() {
		for range time.Tick(5 * time.Minute) {
			CheckIdleConnections()
		}
	}()
}

// CheckIdleConnections disconnects clients idle for too long, based on IdleTimeout setting
func CheckIdleConnections() {
	mutex.RLock()
	defer mutex.RUnlock()
	for _, c := range openConnections {
		idleTime := time.Since(c.lastActivity)
		for _, t := range activeTransfers {
			if t.connectionID == c.ID {
				transferIdleTime := time.Since(t.lastActivity)
				if transferIdleTime < idleTime {
					c.Log(logger.LevelDebug, logSender, "idle time: %v setted to transfer idle time: %v",
						idleTime, transferIdleTime)
					idleTime = transferIdleTime
				}
			}
		}
		if idleTime > idleTimeout {
			err := c.close()
			c.Log(logger.LevelInfo, logSender, "close idle connection, idle time: %v, close error: %v", idleTime, err)
		}
	}
}

func addConnection(c Connection) {
	mutex.Lock()
	defer mutex.Unlock()
	openConnections[c.ID] = c
	metrics.UpdateActiveConnectionsSize(len(openConnections))
	c.Log(logger.LevelDebug, logSender, "connection added, num open connections: %v", len(openConnections))
}

func removeConnection(c Connection) {
	mutex.Lock()
	defer mutex.Unlock()
	delete(openConnections, c.ID)
	metrics.UpdateActiveConnectionsSize(len(openConnections))
	// we have finished to send data here and most of the time the underlying network connection
	// is already closed. Sometime a client can still be reading the last sended data, so we set
	// a deadline instead of directly closing the network connection.
	// Setting a deadline on an already closed connection has no effect.
	// We only need to ensure that a connection will not remain indefinitely open and so the
	// underlying file descriptor is not released.
	// This should protect us against buggy clients and edge cases.
	c.netConn.SetDeadline(time.Now().Add(2 * time.Minute)) //nolint:errcheck
	c.Log(logger.LevelDebug, logSender, "connection removed, num open connections: %v", len(openConnections))
}

func addTransfer(transfer *Transfer) {
	mutex.Lock()
	defer mutex.Unlock()
	activeTransfers = append(activeTransfers, transfer)
}

func removeTransfer(transfer *Transfer) error {
	mutex.Lock()
	defer mutex.Unlock()
	var err error
	indexToRemove := -1
	for i, v := range activeTransfers {
		if v == transfer {
			indexToRemove = i
			break
		}
	}
	if indexToRemove >= 0 {
		activeTransfers[indexToRemove] = activeTransfers[len(activeTransfers)-1]
		activeTransfers = activeTransfers[:len(activeTransfers)-1]
	} else {
		logger.Warn(logSender, transfer.connectionID, "transfer to remove not found!")
		err = fmt.Errorf("transfer to remove not found")
	}
	return err
}

func updateConnectionActivity(id string) {
	mutex.Lock()
	defer mutex.Unlock()
	if c, ok := openConnections[id]; ok {
		c.lastActivity = time.Now()
		openConnections[id] = c
	}
}

func isAtomicUploadEnabled() bool {
	return uploadMode == uploadModeAtomic || uploadMode == uploadModeAtomicWithResume
}

func executeNotificationCommand(a actionNotification) error {
	if !filepath.IsAbs(actions.Hook) {
		err := fmt.Errorf("invalid notification command %#v", actions.Hook)
		logger.Warn(logSender, "", "unable to execute notification command: %v", err)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, actions.Hook, a.Action, a.Username, a.Path, a.TargetPath, a.SSHCmd)
	cmd.Env = append(os.Environ(), a.AsEnvVars()...)
	startTime := time.Now()
	err := cmd.Run()
	logger.Debug(logSender, "", "executed command %#v with arguments: %#v, %#v, %#v, %#v, %#v, elapsed: %v, error: %v",
		actions.Hook, a.Action, a.Username, a.Path, a.TargetPath, a.SSHCmd, time.Since(startTime), err)
	return err
}

func executeAction(a actionNotification) error {
	if !utils.IsStringInSlice(a.Action, actions.ExecuteOn) {
		return errUnconfiguredAction
	}
	if len(actions.Hook) == 0 {
		logger.Warn(logSender, "", "Unable to send notification, no hook is defined")
		return errNoHook
	}
	if strings.HasPrefix(actions.Hook, "http") {
		var url *url.URL
		url, err := url.Parse(actions.Hook)
		if err != nil {
			logger.Warn(logSender, "", "Invalid hook %#v for operation %#v: %v", actions.Hook, a.Action, err)
			return err
		}
		startTime := time.Now()
		httpClient := httpclient.GetHTTPClient()
		resp, err := httpClient.Post(url.String(), "application/json", bytes.NewBuffer(a.AsJSON()))
		respCode := 0
		if err == nil {
			respCode = resp.StatusCode
			resp.Body.Close()
			if respCode != http.StatusOK {
				err = errUnexpectedHTTResponse
			}
		}
		logger.Debug(logSender, "", "notified operation %#v to URL: %v status code: %v, elapsed: %v err: %v",
			a.Action, url.String(), respCode, time.Since(startTime), err)
		return err
	}
	return executeNotificationCommand(a)
}
