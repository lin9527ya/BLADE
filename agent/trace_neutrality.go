package agent

// 目标侧痕迹中性化：模型在目标上创建/发送的用户名、密码、邮箱、文件名、持久化名称、
// User-Agent、回调域名等，都可能被客户、蓝队或测绘引擎看到。它们**不能带本平台的
// 项目特征**——否则一次授权测试就会把"这台机器/这个人属于哪个平台"写在目标上。
//
// 与 P41 出站产物语言同一层：code-owned、随 system 提示词下发、用户改不掉（编辑默认
// 提示词不能把它删掉）。只约束"发给目标/留在目标上的东西"，不约束平台内部记录。
const traceNeutralityBlockText = `

【目标侧痕迹必须中性（硬约束）】
你在目标上创建或发送的任何标识符，都可能被客户、蓝队或测绘引擎看到，并成为"这个平台/操作者是谁"的证据。**不得在本平台之外留下项目特征**：
- 禁止在**用户名、密码、邮箱、文件名/目录名、webshell 或上传物名称、计划任务/服务名、数据库表/字段/记录、User-Agent、自定义 HTTP 头、回调域名与 URL 路径、日志字符串**里出现本平台名称（BLADE / blade）或项目特征（例如 blade-test-marker、blade-shell、blade@…、/opt/blade、/home/*/BLADE）。
- 一律使用与目标业务相符的**中性词**：账号如 svc_backup / ops_test / monitor；文件名如 update.php / cache.aspx / report.pdf；密码用随机字母数字（不要用 Blade@123 这类带品牌的口令）；邮箱用授权方约定的中性测试邮箱。
- 不要把本机路径（含 Code/BLADE、data/tasks 等）复制进发给目标的命令、文件内容或载荷；目标侧路径用目标自己的目录结构。
- 已经留下的带特征痕迹：在 report_artifact 里如实登记（locator 写目标上的绝对路径），尽快清理并 mark_artifact_cleared。
- 目标本身的业务/技术名词（例如 Laravel Blade 模板）不在此列——不要因为读到它们而改写目标内容。
这条只管"发给目标/留在目标上的东西"；你与平台之间的意图、事实、报告照常写。
`

func traceNeutralityBlock() string { return traceNeutralityBlockText }
