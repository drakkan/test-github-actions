package vfs_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"golang.org/x/crypto/ssh"

	"github.com/drakkan/sftpgo/common"
	"github.com/drakkan/sftpgo/config"
	"github.com/drakkan/sftpgo/dataprovider"
	"github.com/drakkan/sftpgo/logger"
	"github.com/drakkan/sftpgo/sftpd"
)

const (
	configDir      = ".."
	sftpServerAddr = "127.0.0.1:3022"
	testUser       = "minio"
	//testUser        = "gcs"
	//testUser        = "azuremul"
	//testUser        = "awss3new"
	defaultPassword = "password"
	testFileName    = "test_file_sftp.dat"
	testDLFileName  = "test_download_sftp.dat"
)

var (
	homeBasePath string
)

func TestMain(m *testing.M) {
	logFilePath := filepath.Join(configDir, "sftpgo_sftpd_test.log")
	logger.InitLogger(logFilePath, 5, 1, 28, false, zerolog.DebugLevel)
	err := config.LoadConfig(configDir, "")
	if err != nil {
		logger.ErrorToConsole("error loading configuration: %v", err)
		os.Exit(1)
	}
	providerConf := config.GetProviderConf()
	logger.InfoToConsole("Starting VFS tests, provider: %v", providerConf.Driver)
	commonConf := config.GetCommonConfig()
	commonConf.UploadMode = 2
	homeBasePath = os.TempDir()

	err = common.Initialize(commonConf)
	if err != nil {
		logger.WarnToConsole("error initializing common: %v", err)
		os.Exit(1)
	}

	err = dataprovider.Initialize(providerConf, configDir, true)
	if err != nil {
		logger.ErrorToConsole("error initializing data provider: %v", err)
		os.Exit(1)
	}

	kmsConfig := config.GetKMSConfig()
	err = kmsConfig.Initialize()
	if err != nil {
		logger.ErrorToConsole("error initializing kms: %v", err)
		os.Exit(1)
	}

	sftpdConf := config.GetSFTPDConfig()
	sftpdConf.Bindings = []sftpd.Binding{
		{
			Port:             3022,
			ApplyProxyConfig: true,
		},
	}
	sftpdConf.EnabledSSHCommands = []string{"*"}

	go func() {
		if err := sftpdConf.Initialize(configDir); err != nil {
			logger.ErrorToConsole("could not start SFTP server: %v", err)
			os.Exit(1)
		}
	}()

	waitTCPListening(sftpdConf.Bindings[0].GetAddress())

	//exitCode := m.Run()
	os.Remove(logFilePath)
	//os.Exit(exitCode)
}

func TestBasicOperations(t *testing.T) {
	user, err := getTestUser(testUser)
	assert.NoError(t, err)
	client, err := getSftpClient(user)
	if assert.NoError(t, err) {
		defer client.Close()
		err = checkBasicSFTP(client)
		assert.NoError(t, err)

		testFilePath := filepath.Join(homeBasePath, testFileName)
		testFileSize := int64(32768)
		err := createTestFile(testFilePath, testFileSize)
		assert.NoError(t, err)
		fileHash, err := computeHashForFile(sha256.New(), testFilePath)
		assert.NoError(t, err)

		err = sftpUploadFile(testFilePath, testFileName, testFileSize, client)
		assert.NoError(t, err)
		localDownloadPath := filepath.Join(homeBasePath, testDLFileName)
		err = sftpDownloadFile(testFileName, localDownloadPath, testFileSize, client)
		assert.NoError(t, err)

		newName := testFileName + "äöühh.txt"
		err = client.Rename(testFileName, newName)
		assert.NoError(t, err)
		info, err := client.Lstat(newName)
		if assert.NoError(t, err) {
			assert.Equal(t, testFileSize, info.Size())
		}
		err = sftpDownloadFile(newName, localDownloadPath, testFileSize, client)
		assert.NoError(t, err)

		resp, err := runSSHCommand(fmt.Sprintf("sha256sum %v", newName), user)
		assert.NoError(t, err)
		assert.Contains(t, string(resp), fileHash)

		res, err := client.ReadDir(".")
		assert.NoError(t, err)
		found := false
		for _, info := range res {
			if info.Name() == newName {
				assert.False(t, info.IsDir())
				found = true
				break
			}
		}
		assert.True(t, found)

		err = client.Remove(newName)
		assert.NoError(t, err)

		// now test operation inside virtual non-existing paths
		subPath := "subtest"
		err = sftpUploadFile(testFilePath, path.Join(subPath, testFileName), testFileSize, client)
		assert.NoError(t, err)
		err = sftpDownloadFile(path.Join(subPath, testFileName), localDownloadPath, testFileSize, client)
		assert.NoError(t, err)

		newName = path.Join(subPath, newName)

		err = client.Rename(path.Join(subPath, testFileName), newName)
		assert.NoError(t, err)
		info, err = client.Lstat(newName)
		if assert.NoError(t, err) {
			assert.Equal(t, testFileSize, info.Size())
			assert.False(t, info.IsDir())
		}
		err = sftpDownloadFile(newName, localDownloadPath, testFileSize, client)
		assert.NoError(t, err)

		info, err = client.Stat(subPath)
		if assert.NoError(t, err) {
			assert.True(t, info.IsDir())
		}

		res, err = client.ReadDir(subPath)
		assert.NoError(t, err)
		found = false
		for _, info := range res {
			if info.Name() == path.Base(newName) {
				assert.False(t, info.IsDir())
				found = true
				break
			}
		}
		assert.True(t, found)

		err = client.RemoveDirectory(subPath)
		assert.Error(t, err)

		err = client.Remove(newName)
		assert.NoError(t, err)

		_, err = client.Stat(newName)
		assert.True(t, errors.Is(err, os.ErrNotExist))

		_, err = client.Stat(subPath)
		assert.True(t, errors.Is(err, os.ErrNotExist))

		err = os.Remove(testFilePath)
		assert.NoError(t, err)
		err = os.Remove(localDownloadPath)
		assert.NoError(t, err)
	}
}

func TestDirCommands(t *testing.T) {
	user, err := getTestUser(testUser)
	assert.NoError(t, err)
	client, err := getSftpClient(user)
	if assert.NoError(t, err) {
		defer client.Close()

		dirName := "diräöütest"
		err = client.Mkdir(dirName)
		assert.NoError(t, err)

		err = client.Rename(dirName, dirName+"1")
		assert.NoError(t, err)

		err = client.Rename(dirName+"1", dirName)
		assert.NoError(t, err)

		info, err := client.Stat(dirName)
		if assert.NoError(t, err) {
			assert.True(t, info.IsDir())
		}

		testFilePath := filepath.Join(homeBasePath, testFileName)
		testFileSize := int64(65535)
		err = createTestFile(testFilePath, testFileSize)
		assert.NoError(t, err)

		err = sftpUploadFile(testFilePath, path.Join(dirName, testFileName), testFileSize, client)
		assert.NoError(t, err)
		localDownloadPath := filepath.Join(homeBasePath, testDLFileName)
		err = sftpDownloadFile(path.Join(dirName, testFileName), localDownloadPath, testFileSize, client)
		assert.NoError(t, err)

		err = client.RemoveDirectory(dirName)
		assert.Error(t, err)

		err = client.Rename(dirName, dirName+"1")
		assert.Error(t, err)

		err = client.Remove(path.Join(dirName, testFileName))
		assert.NoError(t, err)

		err = client.RemoveDirectory(dirName)
		assert.NoError(t, err)
	}
}

func checkBasicSFTP(client *sftp.Client) error {
	_, err := client.Getwd()
	if err != nil {
		return err
	}
	info, err := client.Stat("/")
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("/ must be a directory")
	}
	info, err = client.Stat(".")
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New(". must be a directory")
	}
	_, err = client.ReadDir(".")
	return err
}

func sftpUploadFile(localSourcePath string, remoteDestPath string, expectedSize int64, client *sftp.Client) error {
	srcFile, err := os.Open(localSourcePath)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	destFile, err := client.Create(remoteDestPath)
	if err != nil {
		return err
	}
	_, err = io.Copy(destFile, srcFile)
	if err != nil {
		destFile.Close()
		return err
	}
	destFile.Close()

	if expectedSize > 0 {
		fi, err := client.Stat(remoteDestPath)
		if err != nil {
			return err
		}
		if fi.Size() != expectedSize {
			return fmt.Errorf("uploaded file size does not match, actual: %v, expected: %v", fi.Size(), expectedSize)
		}
	}
	return err
}

func sftpDownloadFile(remoteSourcePath string, localDestPath string, expectedSize int64, client *sftp.Client) error {
	downloadDest, err := os.Create(localDestPath)
	if err != nil {
		return err
	}
	defer downloadDest.Close()
	sftpSrcFile, err := client.Open(remoteSourcePath)
	if err != nil {
		return err
	}
	defer sftpSrcFile.Close()
	_, err = io.Copy(downloadDest, sftpSrcFile)
	if err != nil {
		return err
	}
	err = downloadDest.Sync()
	if err != nil {
		return err
	}
	if expectedSize > 0 {
		fi, err := downloadDest.Stat()
		if err != nil {
			return err
		}
		if fi.Size() != expectedSize {
			return fmt.Errorf("downloaded file size does not match, actual: %v, expected: %v", fi.Size(), expectedSize)
		}
	}
	return err
}

func waitTCPListening(address string) {
	for {
		conn, err := net.Dial("tcp", address)
		if err != nil {
			logger.WarnToConsole("tcp server %v not listening: %v", address, err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		logger.InfoToConsole("tcp server %v now listening", address)
		conn.Close()
		break
	}
}

func createTestFile(path string, size int64) error {
	baseDir := filepath.Dir(path)
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		err = os.MkdirAll(baseDir, os.ModePerm)
		if err != nil {
			return err
		}
	}
	content := make([]byte, size)
	_, err := rand.Read(content)
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, os.ModePerm)
}

func getTestUser(username string) (dataprovider.User, error) {
	return dataprovider.UserExists(username)
}

func getSftpClient(user dataprovider.User) (*sftp.Client, error) {
	var sftpClient *sftp.Client
	config := &ssh.ClientConfig{
		User: user.Username,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil
		},
	}
	config.Auth = []ssh.AuthMethod{ssh.Password(defaultPassword)}

	conn, err := ssh.Dial("tcp", sftpServerAddr, config)
	if err != nil {
		return sftpClient, err
	}
	sftpClient, err = sftp.NewClient(conn)
	return sftpClient, err
}

func runSSHCommand(command string, user dataprovider.User) ([]byte, error) {
	var sshSession *ssh.Session
	var output []byte
	config := &ssh.ClientConfig{
		User: user.Username,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil
		},
	}
	config.Auth = []ssh.AuthMethod{ssh.Password(defaultPassword)}

	conn, err := ssh.Dial("tcp", sftpServerAddr, config)
	if err != nil {
		return output, err
	}
	defer conn.Close()
	sshSession, err = conn.NewSession()
	if err != nil {
		return output, err
	}
	var stdout, stderr bytes.Buffer
	sshSession.Stdout = &stdout
	sshSession.Stderr = &stderr
	err = sshSession.Run(command)
	if err != nil {
		return nil, fmt.Errorf("failed to run command %v: %v", command, stderr.Bytes())
	}
	return stdout.Bytes(), err
}

func computeHashForFile(hasher hash.Hash, path string) (string, error) {
	hash := ""
	f, err := os.Open(path)
	if err != nil {
		return hash, err
	}
	defer f.Close()
	_, err = io.Copy(hasher, f)
	if err == nil {
		hash = fmt.Sprintf("%x", hasher.Sum(nil))
	}
	return hash, err
}
