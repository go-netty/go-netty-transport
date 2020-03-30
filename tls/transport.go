/*
 *  Copyright 2020 the go-netty project
 *
 *  Licensed under the Apache License, Version 2.0 (the "License");
 *  you may not use this file except in compliance with the License.
 *  You may obtain a copy of the License at
 *
 *       https://www.apache.org/licenses/LICENSE-2.0
 *
 *  Unless required by applicable law or agreed to in writing, software
 *  distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  See the License for the specific language governing permissions and
 *  limitations under the License.
 */

package tls

import (
	"crypto/tls"
	"github.com/go-netty/go-netty/transport"
)

type tlsTransport struct {
	*tls.Conn
}

func (t *tlsTransport) Writev(buffs transport.Buffers) (int64, error) {
	return buffs.Buffers.WriteTo(t.Conn)
}

func (t *tlsTransport) Flush() error {
	return nil
}

func (t *tlsTransport) RawTransport() interface{} {
	return t.Conn
}
