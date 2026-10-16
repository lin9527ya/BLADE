package server

import (
	"net/http"

	"github.com/lin9527ya/artex/opsec"
)

// 可检出性知识库一览（P38）：只读。
//
// 为什么要有这个接口：知识库的价值取决于**团队知道它覆盖了什么**——不知道自己手上这份清单的边界，
// 就会把"工具没提示"读成"这一步没有日志"，而那正是这个功能唯一也最危险的失败方式。所以这张清单
// 连同"没收录时怎么算"的口径一起给出来，让人自己看得见边界在哪。
//
// 只读、无参数、不落库：知识库是编译进二进制的常量表（opsec.Knowledge），改它要改代码、过评审。
func (s *Server) opsecKnowledge(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"entries": opsec.Knowledge,
		"note": "这是**事前**的可检出性知识库：给的是「预计会落在哪里」（公开、稳定的日志与遥测常识），" +
			"不是实测结论——实测用 report_detection_check 记录（P31）。命令没被收录时工具会明说「没收录」，" +
			"那不代表没有日志，只代表本库没有依据。本库刻意保守：只收公开、可核对的信号，宁可少收不可编。",
	})
}
