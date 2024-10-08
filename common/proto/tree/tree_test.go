/*
 * Copyright (c) 2019-2021. Abstrium SAS <team (at) pydio.com>
 * This file is part of Pydio Cells.
 *
 * Pydio Cells is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * Pydio Cells is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with Pydio Cells.  If not, see <http://www.gnu.org/licenses/>.
 *
 * The latest code can be found at <https://pydio.com>.
 */

package tree

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"google.golang.org/protobuf/proto"
)

func TestTreeProtoMessage(t *testing.T) {

	Convey("Proto Message", t, func() {
		msg := new(WrappingStreamerResponse)
		msg.Data = &WrappingStreamerResponse_ListNodesResponse{&ListNodesResponse{Node: &Node{Path: "path/folder/file", Type: NodeType_LEAF, Size: 10}}}

		b, err := proto.Marshal(msg)
		So(err, ShouldBeNil)
		So(b, ShouldNotBeEmpty)

		newMsg := new(WrappingStreamerResponse)
		err2 := proto.Unmarshal(b, newMsg)
		So(err2, ShouldBeNil)
	})

}
