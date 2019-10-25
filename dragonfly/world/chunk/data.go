package chunk

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/dragonfly-tech/dragonfly/dragonfly/block/encoder"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"sync"
)

const (
	// SubChunkVersion is the current version of the written sub chunks, specifying the format they are
	// written as over network and on disk.
	SubChunkVersion = 8
)

// SerialisedData holds the serialised data of a chunk. It consists of the chunk's block data itself, a height
// map, the biomes and entities and block entities.
type SerialisedData struct {
	// sub holds the data of the serialised sub chunks in a chunk. Sub chunks that are empty or that otherwise
	// don't exist are represented as an empty slice (or technically, nil).
	SubChunks [16][]byte
	// Data2D is the 2D data of the chunk, which is composed of the biome IDs (256 bytes) and optionally the
	// height map of the chunk.
	Data2D []byte
	// BlockNBT is an encoded NBT array of all blocks that carry additional NBT, such as chests, with all
	// their contents.
	BlockNBT []byte
}

// pool is used to pool byte buffers used for encoding chunks.
var pool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, 1024))
	},
}

// NetworkEncode encodes a chunk passed to its network representation and returns it as a SerialisedData,
// which may be sent over network.
func NetworkEncode(c *Chunk) (d SerialisedData) {
	buf := pool.Get().(*bytes.Buffer)

	for y, sub := range c.sub {
		if sub == nil {
			// No need to put empty sub chunks in the SerialisedData.
			continue
		}
		_ = buf.WriteByte(SubChunkVersion)
		_ = buf.WriteByte(byte(len(sub.storages)))
		for _, storage := range sub.storages {
			_ = buf.WriteByte(byte(storage.bitsPerBlock<<1) | 1)
			for _, word := range storage.blocks {
				_ = binary.Write(buf, binary.LittleEndian, word)
			}
			_ = protocol.WriteVarint32(buf, int32(storage.palette.Len()))
			for _, runtimeID := range storage.palette.blockRuntimeIDs {
				_ = protocol.WriteVarint32(buf, int32(runtimeID))
			}
		}
		d.SubChunks[y] = make([]byte, buf.Len())
		_, _ = buf.Read(d.SubChunks[y])
	}
	d.Data2D = append(c.biomes[:], 0)

	buf.Reset()
	pool.Put(buf)
	return
}

// emptyHeightMap is saved for the height map while it is not implemented.
var emptyHeightMap = make([]byte, 512)

// DiskEncode encodes a chunk to its disk representation, so that it may be stored in a database, giving other
// servers the ability to read the chunk.
func DiskEncode(c *Chunk) (d SerialisedData) {
	buf := pool.Get().(*bytes.Buffer)
	for y, sub := range c.sub {
		if sub == nil {
			// The sub chunk at this Y value is empty, so don't write it.
			continue
		}
		_ = buf.WriteByte(SubChunkVersion)
		_ = buf.WriteByte(byte(len(sub.storages)))
		for _, storage := range sub.storages {
			diskEncodeBlockStorage(buf, storage)
		}
		d.SubChunks[y] = append([]byte(nil), buf.Bytes()...)
		buf.Reset()
	}
	// We simply keep a zero slice for the height map, as we don't currently build it up.
	// TODO: Implement calculation of height maps of chunks.
	buf.Write(emptyHeightMap)
	buf.Write(c.biomes[:])
	d.Data2D = append([]byte(nil), buf.Bytes()...)
	buf.Reset()

	// TODO: Add block NBT and save them to the chunk here.
	d.BlockNBT = nil

	pool.Put(buf)
	return d
}

// DiskDecode decodes the data from a SerialisedData object into a chunk and returns it. If the data was
// invalid, an error is returned.
func DiskDecode(data SerialisedData) (*Chunk, error) {
	c := &Chunk{}
	copy(c.biomes[:], data.Data2D[512:])

	// TODO: Read block NBT into chunk here.

	for y, sub := range data.SubChunks {
		if len(sub) == 0 {
			// No data for this sub chunk.
			continue
		}
		buf := bytes.NewBuffer(sub)
		ver, err := buf.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("error reading version: %v", err)
		}
		c.sub[y] = &SubChunk{}
		switch ver {
		default:
			return nil, fmt.Errorf("unknown sub chunk version %v: can't decode", ver)
		case 1:
			// Version 1 only has one layer for each sub chunk, but uses the format with palettes.
			storage, err := diskDecodeBlockStorage(buf)
			if err != nil {
				return nil, err
			}
			c.sub[y].storages = append(c.sub[y].storages, storage)
		case 8:
			// Version 8 allows up to 256 layers for one sub chunk.
			storageCount, err := buf.ReadByte()
			if err != nil {
				return nil, fmt.Errorf("error reading storage count: %v", err)
			}
			c.sub[y].storages = make([]*BlockStorage, storageCount)

			for i := byte(0); i < storageCount; i++ {
				c.sub[y].storages[i], err = diskDecodeBlockStorage(buf)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	return c, nil
}

// block represents a block as found in a disk save of a world.
type block struct {
	Name string `nbt:"name"`
	Data int16  `nbt:"val"`
}

// diskEncodeBlockStorage encodes a block storage to its disk representation into the buffer passed.
func diskEncodeBlockStorage(buf *bytes.Buffer, storage *BlockStorage) {
	_ = buf.WriteByte(byte(storage.bitsPerBlock<<1) | 0)
	for _, block := range storage.blocks {
		_ = binary.Write(buf, binary.LittleEndian, block)
	}
	_ = binary.Write(buf, binary.LittleEndian, int32(storage.palette.Len()))

	blocks := make([]block, storage.palette.Len())
	for index, runtimeID := range storage.palette.blockRuntimeIDs {
		// Get the block state registered with the runtime IDs we have in the palette of the block storage
		// as we need the name and data value to store.
		b := encoder.Blocks[runtimeID]
		blocks[index] = block{
			Name: b.Name,
			Data: b.Data,
		}
	}
	// Marshal the slice of block states into NBT and add it to the byte slice.
	enc := nbt.NewEncoder(buf)
	enc.Variant = nbt.LittleEndian
	for _, block := range blocks {
		_ = enc.Encode(block)
	}
}

// diskDecodeBlockStorage decodes a block storage from the buffer passed. If not successful, an error is
// returned.
func diskDecodeBlockStorage(buf *bytes.Buffer) (*BlockStorage, error) {
	blockSize, err := buf.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("error reading block size: %v", err)
	}
	blockSize >>= 1

	// blocksPerUint32 is the amount of blocks that may be stored in a single uint32.
	blocksPerUint32 := 32 / int(blockSize)

	// uint32Count is the amount of uint32s required to store all blocks: 4096 blocks need to be stored in
	// total.
	uint32Count := 4096 / blocksPerUint32

	if blockSize == 3 || blockSize == 5 || blockSize == 6 {
		// We've got one of the padded sizes, so the block storage has another uint32 to be able to store
		// every block.
		uint32Count++
	}

	uint32s := make([]uint32, uint32Count)

	data := buf.Next(uint32Count * 4)
	if len(data) != uint32Count*4 {
		return nil, fmt.Errorf("cannot read block storage: not enough block data present: expected %v bytes, got %v", uint32Count*4, len(data))
	}

	for i := 0; i < uint32Count; i++ {
		// Explicitly don't use the binary package to greatly improve performance of reading the uint32s.
		uint32s[i] = uint32(data[i*4]) | uint32(data[i*4+1])<<8 | uint32(data[i*4+2])<<16 | uint32(data[i*4+3])<<24
	}

	// The next 4 bytes are an LE int32, but we simply read it and decode the int32 ourselves, as it's much
	// faster here.
	data = buf.Next(4)
	if len(data) != 4 {
		return nil, fmt.Errorf("cannot read palette entry count: expected 4 bytes, got %v", len(data))
	}
	paletteCount := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24

	blocks := make([]block, paletteCount)

	dec := nbt.NewDecoder(buf)
	dec.Variant = nbt.LittleEndian

	// There are paletteCount NBT tags that represent unique blocks.
	for i := uint32(0); i < paletteCount; i++ {
		if err := dec.Decode(&blocks[i]); err != nil {
			return nil, fmt.Errorf("error decoding block: %v", err)
		}
	}

	palette := &palette{blockRuntimeIDs: make([]uint32, paletteCount)}
	for i, b := range blocks {
		// Finally we add the runtime ID of the block to the palette we create.
		palette.blockRuntimeIDs[i] = encoder.RuntimeIDs[protocol.BlockEntry{
			Name: b.Name,
			Data: b.Data,
		}]
	}
	return newBlockStorage(uint32s, palette), nil
}