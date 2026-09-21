package environmentbuild

// PhaseError carries a machine classification only. Never put a command, URL,
// package-manager diagnostic, registry response, or credential in this field.
// Unknown codes produce the same fixed fallback as unclassified errors.
type PhaseError struct { Code string }
func(e *PhaseError)Error()string{if e!=nil&&phaseMessage(e.Code)!=""{return e.Code};return "environment phase failed"}
func(e *PhaseError)UserMessage()string{if e==nil{return ""};return phaseMessage(e.Code)}
func phaseMessage(code string)string{
 switch code{
 case "UNSUPPORTED_WORKSPACE":return "此调试环境不支持保存训练镜像；请使用平台配套 Base 调试环境，原有训练 Base 镜像仍可正常使用"
 case "ENVIRONMENT_CHANGED":return "捕获期间依赖环境发生变化；请停止安装或修改依赖，再重新保存环境"
 case "WHEEL_UNAVAILABLE":return "部分依赖在配置的内部源中没有可用 wheel；请使用内部源已有的兼容版本，或联系管理员将所需 wheel 加入内部源后重新保存"
 case "PACKAGE_MODIFIED":return "检测到基础包覆盖、手工修改或不可重建的安装；请恢复基础环境，并将新增依赖以受管 wheel 方式安装后重新保存"
 case "BUILD_TIMEOUT":return "环境捕获或构建超时；请检查依赖规模和集群资源后重试。已捕获的依赖版本不会因重试而改变"
 case "PULL_FAILED":return "镜像已推送，但训练集群暂时无法拉取或完成自检；请联系管理员检查拉取机器人权限和镜像兼容性，再重试验证"
 case "TEMP_STORAGE_FULL":return "环境构建临时盘空间不足；请减小新增依赖规模或联系管理员调整构建存储限制，再重新保存环境"
 default:return ""
 }
}
