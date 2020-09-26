package common

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/drakkan/sftpgo/dataprovider"
	"github.com/drakkan/sftpgo/vfs"
)

func TestNewActionNotification(t *testing.T) {
	user := &dataprovider.User{
		Username: "username",
	}
	user.FsConfig.Provider = 0
	user.FsConfig.S3Config = vfs.S3FsConfig{
		Bucket:   "s3bucket",
		Endpoint: "endpoint",
	}
	user.FsConfig.GCSConfig = vfs.GCSFsConfig{
		Bucket: "gcsbucket",
	}
	a := newActionNotification(user, operationDownload, "path", "target", "", ProtocolSFTP, 123, errors.New("fake error"))
	assert.Equal(t, user.Username, a.Username)
	assert.Equal(t, 0, len(a.Bucket))
	assert.Equal(t, 0, len(a.Endpoint))
	assert.Equal(t, 0, a.Status)

	user.FsConfig.Provider = 1
	a = newActionNotification(user, operationDownload, "path", "target", "", ProtocolSSH, 123, nil)
	assert.Equal(t, "s3bucket", a.Bucket)
	assert.Equal(t, "endpoint", a.Endpoint)
	assert.Equal(t, 1, a.Status)

	user.FsConfig.Provider = 2
	a = newActionNotification(user, operationDownload, "path", "target", "", ProtocolSCP, 123, ErrQuotaExceeded)
	assert.Equal(t, "gcsbucket", a.Bucket)
	assert.Equal(t, 0, len(a.Endpoint))
	assert.Equal(t, 2, a.Status)
}

func TestActionHTTP(t *testing.T) {
	actionsCopy := Config.Actions

	Config.Actions = ProtocolActions{
		ExecuteOn: []string{operationDownload},
		Hook:      fmt.Sprintf("http://%v", httpAddr),
	}
	user := &dataprovider.User{
		Username: "username",
	}
	a := newActionNotification(user, operationDownload, "path", "target", "", ProtocolSFTP, 123, nil)
	err := a.execute()
	assert.NoError(t, err)

	Config.Actions.Hook = "http://invalid:1234"
	err = a.execute()
	assert.Error(t, err)

	Config.Actions.Hook = fmt.Sprintf("http://%v/404", httpAddr)
	err = a.execute()
	if assert.Error(t, err) {
		assert.EqualError(t, err, errUnexpectedHTTResponse.Error())
	}

	Config.Actions = actionsCopy
}

func TestActionCMD(t *testing.T) {
	if runtime.GOOS == osWindows {
		t.Skip("this test is not available on Windows")
	}
	actionsCopy := Config.Actions

	hookCmd, err := exec.LookPath("true")
	assert.NoError(t, err)

	Config.Actions = ProtocolActions{
		ExecuteOn: []string{operationDownload},
		Hook:      hookCmd,
	}
	user := &dataprovider.User{
		Username: "username",
	}
	a := newActionNotification(user, operationDownload, "path", "target", "", ProtocolSFTP, 123, nil)
	err = a.execute()
	assert.NoError(t, err)

	SSHCommandActionNotification(user, "path", "target", "sha1sum", nil)

	Config.Actions = actionsCopy
}

func TestWrongActions(t *testing.T) {
	actionsCopy := Config.Actions

	badCommand := "/bad/command"
	if runtime.GOOS == osWindows {
		badCommand = "C:\\bad\\command"
	}
	Config.Actions = ProtocolActions{
		ExecuteOn: []string{operationUpload},
		Hook:      badCommand,
	}
	user := &dataprovider.User{
		Username: "username",
	}

	a := newActionNotification(user, operationUpload, "", "", "", ProtocolSFTP, 123, nil)
	err := a.execute()
	assert.Error(t, err, "action with bad command must fail")

	a.Action = operationDelete
	err = a.execute()
	assert.EqualError(t, err, errUnconfiguredAction.Error())

	Config.Actions.Hook = "http://foo\x7f.com/"
	a.Action = operationUpload
	err = a.execute()
	assert.Error(t, err, "action with bad url must fail")

	Config.Actions.Hook = ""
	err = a.execute()
	if assert.Error(t, err) {
		assert.EqualError(t, err, errNoHook.Error())
	}

	Config.Actions.Hook = "relative path"
	err = a.execute()
	if assert.Error(t, err) {
		assert.EqualError(t, err, fmt.Sprintf("invalid notification command %#v", Config.Actions.Hook))
	}

	Config.Actions = actionsCopy
}

func TestPreDeleteAction(t *testing.T) {
	if runtime.GOOS == osWindows {
		t.Skip("this test is not available on Windows")
	}
	actionsCopy := Config.Actions

	hookCmd, err := exec.LookPath("true")
	assert.NoError(t, err)
	Config.Actions = ProtocolActions{
		ExecuteOn: []string{operationPreDelete},
		Hook:      hookCmd,
	}
	homeDir := filepath.Join(os.TempDir(), "test_user")
	err = os.MkdirAll(homeDir, os.ModePerm)
	assert.NoError(t, err)
	user := dataprovider.User{
		Username: "username",
		HomeDir:  homeDir,
	}
	user.Permissions = make(map[string][]string)
	user.Permissions["/"] = []string{dataprovider.PermAny}
	fs := vfs.NewOsFs("id", homeDir, nil)
	c := NewBaseConnection("id", ProtocolSFTP, user, fs)

	testfile := filepath.Join(user.HomeDir, "testfile")
	err = ioutil.WriteFile(testfile, []byte("test"), os.ModePerm)
	assert.NoError(t, err)
	info, err := os.Stat(testfile)
	assert.NoError(t, err)
	err = c.RemoveFile(testfile, "testfile", info)
	assert.NoError(t, err)
	assert.FileExists(t, testfile)

	os.RemoveAll(homeDir)

	Config.Actions = actionsCopy
}
