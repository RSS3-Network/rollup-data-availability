package near

/*
#include "./lib/libnear_da_rpc_sys.h"
#include <stdlib.h>
#include <string.h>
*/
import "C"

import (
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"unsafe"

	"github.com/ethereum/go-ethereum/log"
	sidecar "github.com/near/rollup-data-availability/gopkg/sidecar"
)

type Namespace struct {
	Version uint8
	Id      uint32
}

type Config struct {
	Namespace Namespace
	Client    *C.Client
}

var (
	ErrInvalidSize    = errors.New("NEAR DA unmarshal blob: invalid size")
	ErrInvalidNetwork = errors.New("NEAR DA client relative URL without a base")
)

// Framer defines a way to encode/decode a FrameRef.
type Framer interface {
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
}

// BlobRef contains the reference to the specific blob on near and
// satisfies the Framer interface.
type BlobRef struct {
	TxId []byte
}

var _ Framer = &BlobRef{}

// MarshalBinary encodes the Ref into a format that can be
// serialized.
func (f *BlobRef) MarshalBinary() ([]byte, error) {
	ref := make([]byte, sidecar.EncodedBlobRefSize)

	copy(ref[:sidecar.EncodedBlobRefSize], f.TxId)

	return ref, nil
}

func (f *BlobRef) UnmarshalBinary(ref []byte) error {
	if len(ref) != sidecar.EncodedBlobRefSize {
		log.Warn("invalid size ", len(ref), " expected ", sidecar.EncodedBlobRefSize)
		return ErrInvalidSize
	}
	f.TxId = ref[:sidecar.EncodedBlobRefSize]
	return nil
}

// Note, networkN value can be either Mainnet, Testnet
// or loopback address in [ip]:[port] format.
func NewConfig(accountN, contractN, keyN, networkN string, ns uint32) (*Config, error) {
	log.Info("creating NEAR client", "account", accountN, "contract", contractN, "network", networkN, "namespace", ns)

	account := C.CString(accountN)
	defer C.free(unsafe.Pointer(account))

	key := C.CString(keyN)
	defer C.free(unsafe.Pointer(key))

	contract := C.CString(contractN)
	defer C.free(unsafe.Pointer(contract))

	network := C.CString(networkN)
	defer C.free(unsafe.Pointer(network))

	// Numbers don't need to be dellocated
	namespaceId := C.uint(ns)
	namespaceVersion := C.uint8_t(0)

	daClient := C.new_client(account, key, contract, network, namespaceVersion, namespaceId)
	if daClient == nil {
		err := GetDAError()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("unable to create NEAR DA client")
	}

	return &Config{
		Namespace: Namespace{Version: 0, Id: ns},
		Client:    daClient,
	}, nil
}

// Note, networkN value can be either Mainnet, Testnet
// or loopback address in [ip]:[port] format.
func NewConfigFile(keyPathN, contractN, networkN string, ns uint32) (*Config, error) {
	keyPath := C.CString(keyPathN)
	defer C.free(unsafe.Pointer(keyPath))

	contract := C.CString(contractN)
	defer C.free(unsafe.Pointer(contract))

	network := C.CString(networkN)
	defer C.free(unsafe.Pointer(network))

	namespaceId := C.uint(ns)
	namespaceVersion := C.uint8_t(0)

	daClient := C.new_client_file(keyPath, contract, network, namespaceVersion, namespaceId)
	if daClient == nil {
		err := GetDAError()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("unable to create NEAR DA client")
	}

	return &Config{
		Namespace: Namespace{Version: 0, Id: ns},
		Client:    daClient,
	}, nil
}

// Note, candidateHex has to be "0xfF00000000000000000000000000000000000000" for the
// data to be submitted in the case of other Rollups. If concerned, use ForceSubmit
func (config *Config) Submit(candidateHex string, data []byte) ([]byte, error) {
	candidateHexPtr := C.CString(candidateHex)
	defer C.free(unsafe.Pointer(candidateHexPtr))

	txBytes := C.CBytes(data)
	defer C.free(unsafe.Pointer(txBytes))

	maybeFrameRef := C.submit_batch(config.Client, candidateHexPtr, (*C.uint8_t)(txBytes), C.size_t(len(data)))

	if err := GetDAError(); err != nil {
		return nil, err
	}

	log.Info("Submitting to NEAR",
		"maybeFrameData", maybeFrameRef,
		"candidate", candidateHex,
		"namespace", config.Namespace,
		"txLen", C.size_t(len(data)),
	)

	if maybeFrameRef.len > 1 {
		// Set the tx data to a frame reference
		frameData := C.GoBytes(unsafe.Pointer(maybeFrameRef.data), C.int(maybeFrameRef.len))
		return frameData, nil
	}

	log.Warn("no frame reference returned from NEAR")
	return nil, fmt.Errorf("no frame reference returned from NEAR")
}

// Used by other rollups without candidate semantics, if you know for sure you want to submit the
// data to NEAR
func (config *Config) ForceSubmit(data []byte) ([]byte, error) {
	candidateHex := "0xfF00000000000000000000000000000000000000"
	return config.Submit(candidateHex, data)
}

func (config *Config) Get(frameRefBytes []byte, txIndex uint32) ([]byte, error) {
	frameRef := BlobRef{}
	if err := frameRef.UnmarshalBinary(frameRefBytes); err != nil {
		log.Warn("unable to decode frame reference", "index", txIndex, "err", err)
		return nil, err
	}

	log.Info("NEAR frameRef request", "txId", hex.EncodeToString(frameRef.TxId), "TxCommitment", hex.EncodeToString(frameRef.TxCommitment))

	txId := C.CBytes(frameRef.TxId)
	defer C.free(unsafe.Pointer(txId))

	blob := C.get((*C.Client)(config.Client), (*C.uint8_t)(txId))
	defer C.free(unsafe.Pointer(blob))

	if blob == nil {
		if err := GetDAError(); err != nil {
			log.Warn("no data returned from near", "txId", hex.EncodeToString(frameRef.TxId))
			return nil, err
		}
		return nil, errors.New("blob is nil from near")
	}

	return ToBytes(blob), nil
}

func (config *Config) FreeClient() {
	C.free_client((*C.Client)(config.Client))
	config.Client = nil
}

func NewBlobSafe(data []byte) *C.BlobSafe {
	blob := C.BlobSafe{
		data: (*C.uint8_t)(C.CBytes(data)),
		len:  C.size_t(len(data)),
	}
	return &blob
}

func ToBytes(b *C.BlobSafe) []byte {
	return C.GoBytes(unsafe.Pointer(b.data), C.int(b.len))
}

func To32Bytes(ptr unsafe.Pointer) []byte {
	bytes := make([]byte, 32)
	copy(bytes, C.GoBytes(ptr, 32))

	return bytes
}

func GetDAError() (err error) {
	defer func() {
		if rErr := recover(); rErr != nil {
			err = fmt.Errorf("critical error from NEAR DA GetDAError: %v", rErr)
		}
	}()

	errData := C.get_error()
	if errData == nil || unsafe.Pointer(errData) == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(errData))
	defer C.clear_error()

	errStr := C.GoString(errData)
	return fmt.Errorf("NEAR DA client %v", errStr)
}

func TestSetError(msg string) {
	cmsg := C.CString(msg)
	defer C.free(unsafe.Pointer(cmsg))
	C.set_error(cmsg)
}
