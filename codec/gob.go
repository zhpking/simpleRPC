package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

type GobCodec struct {
	conn io.ReadWriteCloser
	buf *bufio.Writer
	dec *gob.Decoder
	enc *gob.Encoder
}

func (c *GobCodec) Close() error {
	return c.conn.Close()
}

func (c *GobCodec) ReadHeader(h *Header) error {
	return c.dec.Decode(h)
}

func (c *GobCodec) ReadBody(body interface{}) error {
	return c.dec.Decode(body)
}

func (c *GobCodec) Write(h *Header, body interface{}) (err error) {
	defer func() {
		_ = c.buf.Flush()
		if err != nil {
			_ = c.Close()
		}
	}()

	if err := c.enc.Encode(h); err != nil {
		log.Println("rpc codec: gob error encoding header:", err)
		return err
	}

	if err := c.enc.Encode(body); err != nil {
		log.Println("rpc codec: gob error encoding body:", err)
		return err
	}

	return nil
}

// 1. nil值其实也有类型的，(*int)(nil)和(interface{})(nil)就是两个不同的变量，它们也不相等
//    所以(*GobCodec)(nil)可以理解为nil定义为*GobCodec类型
// 2. _ Codec 格式：_ 接口 声明了=后面的类型，不必须要实现_ 后面的接口，否则编译不通过
//    这里声明了GobCodec 必须要实现Codec接口
var _ Codec = (*GobCodec)(nil)

// 解码方法
func NewGobCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf: buf,
		dec: gob.NewDecoder(conn),
		// bufio是先把数据放到缓存区里，等Flush的时候在一次发送过去
		enc: gob.NewEncoder(buf),
	}
}
