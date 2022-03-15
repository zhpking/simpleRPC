package codec

import "io"

type Header struct {
	ServiceMethod string // 格式：Service.Method，就像别的rpc框架一样，远程调用的path
	Seq uint64
	Error string
}

type Codec interface {
	// 相当于继承io.Closer接口，所以实现Codec的时候，需要实现接口io.Closer里的Close()方法
	io.Closer
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

//  io.ReadWriteCloser 是对基本的 Read，Write 和 Close 方法进行分组的接口
type NewCodecFunc func(io.ReadWriteCloser) Codec

type Type string

const (
	GobType Type = "application/gob"
	JsonType Type = "application/json"
)

var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
}
