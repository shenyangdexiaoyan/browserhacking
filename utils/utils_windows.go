package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"hack-browser-data/log"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/tidwall/gjson"
	"golang.org/x/crypto/pbkdf2"
)

const (
	chromeProfilePath    = "/AppData/Local/Google/Chrome/User Data/*/"
	chromeKeyPath        = "/AppData/Local/Google/Chrome/User Data/Local State"
	edgeProfilePath      = "/AppData/Local/Microsoft/Edge/User Data/*/"
	edgeKeyPath          = "/AppData/Local/Microsoft/Edge/User Data/Local State"
	speed360ProfilePath  = "/AppData/Local/360chrome/Chrome/User Data/*/"
	speed360KeyPath      = ""
	qqBrowserProfilePath = "/AppData/Local/Tencent/QQBrowser/User Data/*/"
	qqBrowserKeyPath     = ""
	firefoxProfilePath   = "/AppData/Roaming/Mozilla/Firefox/Profiles/*.default-release/"
	firefoxKeyPath       = ""
)

var (
	chromeKey []byte

	browserList = map[string]struct {
		ProfilePath string
		KeyPath     string
	}{
		"chrome": {
			chromeProfilePath,
			chromeKeyPath,
		},
		"edge": {
			edgeProfilePath,
			edgeKeyPath,
		},
		"360speed": {
			speed360ProfilePath,
			speed360KeyPath,
		},
		"qq": {
			qqBrowserProfilePath,
			qqBrowserKeyPath,
		},
		"firefox": {
			firefoxProfilePath,
			"",
		},
	}
)

func PickBrowser(name string) (browserDir, key string, err error) {
	name = strings.ToLower(name)
	if choice, ok := browserList[name]; ok {
		if choice.KeyPath != "" {
			return os.Getenv("USERPROFILE") + choice.ProfilePath, os.Getenv("USERPROFILE") + choice.KeyPath, nil
		} else {
			return os.Getenv("USERPROFILE") + choice.ProfilePath, "", nil
		}
	}
	return "", "", errBrowserNotSupported
}

var (
	errBase64DecodeFailed = errors.New("decode base64 failed")
)

func InitKey(key string) error {
	if key == "" {
		VersionUnder80 = true
		return nil
	}
	keyFile, err := ReadFile(key)
	if err != nil {
		return err
	}
	encryptedKey := gjson.Get(keyFile, "os_crypt.encrypted_key")
	if encryptedKey.Exists() {
		pureKey, err := base64.StdEncoding.DecodeString(encryptedKey.String())
		if err != nil {
			return errBase64DecodeFailed
		}
		chromeKey, err = decryptStringWithDPAPI(pureKey[5:])
		return nil
	} else {
		VersionUnder80 = true
		return nil
	}
}

func DecryptChromePass(encryptPass []byte) (string, error) {
	if len(encryptPass) > 15 {
		// remove prefix 'v10'
		return aesGCMDecrypt(encryptPass[15:], chromeKey, encryptPass[3:15])
	} else {
		return "", errPasswordIsEmpty
	}
}

// chromium > 80 https://source.chromium.org/chromium/chromium/src/+/master:components/os_crypt/os_crypt_win.cc
func aesGCMDecrypt(crypted, key, nounce []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	blockMode, err := cipher.NewGCM(block)
	origData, err := blockMode.Open(nil, nounce, crypted, nil)
	if err != nil {
		return "", err
	}
	return string(origData), nil
}

type DataBlob struct {
	cbData uint32
	pbData *byte
}

func NewBlob(d []byte) *DataBlob {
	if len(d) == 0 {
		return &DataBlob{}
	}
	return &DataBlob{
		pbData: &d[0],
		cbData: uint32(len(d)),
	}
}

func (b *DataBlob) ToByteArray() []byte {
	d := make([]byte, b.cbData)
	copy(d, (*[1 << 30]byte)(unsafe.Pointer(b.pbData))[:])
	return d
}

// chrome < 80 https://chromium.googlesource.com/chromium/src/+/76f496a7235c3432983421402951d73905c8be96/components/os_crypt/os_crypt_win.cc#82
func decryptStringWithDPAPI(data []byte) ([]byte, error) {
	dllCrypt := syscall.NewLazyDLL("Crypt32.dll")
	dllKernel := syscall.NewLazyDLL("Kernel32.dll")
	procDecryptData := dllCrypt.NewProc("CryptUnprotectData")
	procLocalFree := dllKernel.NewProc("LocalFree")
	var outBlob DataBlob
	r, _, err := procDecryptData.Call(uintptr(unsafe.Pointer(NewBlob(data))), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&outBlob)))
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.pbData)))
	return outBlob.ToByteArray(), nil
}

func DecryptStringWithDPAPI(data []byte) (string, error) {
	dllCrypt := syscall.NewLazyDLL("Crypt32.dll")
	dllKernel := syscall.NewLazyDLL("Kernel32.dll")
	procDecryptData := dllCrypt.NewProc("CryptUnprotectData")
	procLocalFree := dllKernel.NewProc("LocalFree")
	var outBlob DataBlob
	r, _, err := procDecryptData.Call(uintptr(unsafe.Pointer(NewBlob(data))), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&outBlob)))
	if r == 0 {
		return "", err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.pbData)))
	return string(outBlob.ToByteArray()), nil
}

type MetaPBE struct {
	SequenceA
	CipherText []byte
}
type SequenceA struct {
	PKCS5PBES2 asn1.ObjectIdentifier
	SequenceB
}
type SequenceB struct {
	SequenceC
	SequenceD
}

type SequenceC struct {
	PKCS5PBKDF2 asn1.ObjectIdentifier
	SequenceE
}

type SequenceD struct {
	AES256CBC asn1.ObjectIdentifier
	IV        []byte
}

type SequenceE struct {
	EntrySalt      []byte
	IterationCount int
	KeySize        int
	SequenceF
}

type SequenceF struct {
	HMACWithSHA256 asn1.ObjectIdentifier
}

func CheckPassword(globalSalt, masterPwd []byte, pbe MetaPBE) ([]byte, error) {
	sha1.New()
	k := sha1.Sum(globalSalt)

	log.Println(hex.EncodeToString(k[:]))
	key := pbkdf2.Key(k[:], pbe.EntrySalt, pbe.IterationCount, pbe.KeySize, sha256.New)
	log.Println(hex.EncodeToString(key))
	i, err := hex.DecodeString("040e")
	if err != nil {
		log.Println(err)
	}
	// @https://hg.mozilla.org/projects/nss/rev/fc636973ad06392d11597620b602779b4af312f6#l6.49
	iv := append(i, pbe.IV...)
	dst, err := aes128CBCDecrypt(key, iv, pbe.CipherText)
	if err != nil {
		log.Println(err)
	}
	return dst, err
}

func DecodeMeta(decodeItem []byte) (pbe MetaPBE, err error) {
	_, err = asn1.Unmarshal(decodeItem, &pbe)
	if err != nil {
		log.Error(err)
		return
	}
	return
}
