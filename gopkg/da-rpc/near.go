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
	"reflect"
	"unsafe"

	"github.com/ethereum/go-ethereum/log"
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
	ErrInvalidSize    = errors.New("invalid size")
	ErrInvalidNetwork = errors.New("invalid network")
)

// Framer defines a way to encode/decode a FrameRef.
type Framer interface {
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
}

// FrameRef contains the reference to the specific blob on near and
// satisfies the Framer interface.
type FrameRef struct {
	TxId         []byte
	TxCommitment []byte
}

var _ Framer = &FrameRef{}

// MarshalBinary encodes the FrameRef
//
//	----------------------------------------
//
// | 32 byte txid  |  32 byte commitment   |
//
//	----------------------------------------
//
// | <-- txid --> | <-- commitment -->    |
//
//	----------------------------------------
func (f *FrameRef) MarshalBinary() ([]byte, error) {
	ref := make([]byte, len(f.TxId)+len(f.TxCommitment))

	copy(ref[:32], f.TxId)
	copy(ref[32:], f.TxCommitment)

	return ref, nil
}

// UnmarshalBinary decodes the binary to FrameRef
// serialization format: height + commitment
//
//	----------------------------------------
//
// | 32 byte txid  |  32 byte commitment   |
//
//	----------------------------------------
//
// | <-- txid --> | <-- commitment -->    |
//
//	----------------------------------------
func (f *FrameRef) UnmarshalBinary(ref []byte) error {
	if len(ref) < 64 {
		return ErrInvalidSize
	}
	f.TxId = ref[:32]
	f.TxCommitment = ref[32:]
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
func (config *Config) Submit(candidateHex string, data []byte) (frameData []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic recovered from Near Submit: %v", r)
		}
	}()

	candidateHexPtr := C.CString(candidateHex)
	defer C.free(unsafe.Pointer(candidateHexPtr))

	txBytes := C.CBytes(data)
	defer C.free(unsafe.Pointer(txBytes))

	maybeFrameRef := C.submit_batch(config.Client, candidateHexPtr, (*C.uint8_t)(txBytes), C.size_t(len(data)))

	if err = GetDAError(); err != nil {
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
		frameData = C.GoBytes(unsafe.Pointer(maybeFrameRef.data), C.int(maybeFrameRef.len))
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
	frameRef := FrameRef{}
	if err := frameRef.UnmarshalBinary(frameRefBytes); err != nil {
		log.Warn("unable to decode frame reference ", "index", txIndex, "err", err)
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

	log.Info("NEAR data retrieved", "txId", hex.EncodeToString(frameRef.TxId))

	commitment := To32Bytes(unsafe.Pointer(&blob.commitment))

	if !reflect.DeepEqual(commitment, frameRef.TxCommitment) {
		return nil, errors.New("NEAR commitments don't match")
	} else {
		log.Debug("Blob commitments match!")
		return ToBytes(blob), nil
	}
}

func (config *Config) FreeClient() {
	C.free_client((*C.Client)(config.Client))
	config.Client = nil
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
	errData := C.get_error()
	if errData == nil || unsafe.Pointer(errData) == nil {
		return nil
	}

	len := C.strlen(errData)
	goString := C.GoStringN(errData, C.int(len))
	C.free(unsafe.Pointer(errData))

	return fmt.Errorf("NEAR DA client %v", goString)
}
