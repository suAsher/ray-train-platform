# Portal 文件上传 405 修复与验证

用户在“数据与存储”上传本地权重时出现405。该问题属于Portal上传路由错误，与自动化浏览器工具拒绝读取本机文件的工作目录限制不同。

## 原因与影响范围

后端上传票据返回 `/api/v1/data-spaces/<space>/content?path=...`。普通API客户端会添加Portal网关前缀 `/raytrain`，文件上传的XMLHttpRequest却直接使用票据URL，导致请求进入Portal静态站点。分片PUT手工拼接的路径同样缺前缀；创建票据、完成及中止会话原本走统一客户端，不受影响。

生产空请求验证使用不存在的 `route-probe` 数据空间，未写文件：无前缀PUT返回Nginx405 HTML；正确前缀PUT返回后端404 JSON `DATA_SPACE_NOT_FOUND`。随后在本人工作区，用浏览器生成的156字节专用文本File触发真实页面上传：创建票据201，文件PUT405，目录确认文件未入库。没有读取自动化工具拒绝访问的本地ZIP，也没有改写现有文件。

修复仅在独立Portal `dev`：为数据空间上传URL补网关前缀，保留全部查询字节、已有前缀、外部预签名URL；分片PUT使用同一函数。源码ZIP原本使用 `/raytrain/ray/api/packages/gcs/...`，保持原样。没有改请求头、身份、数据权限、后端、Ingress或训练镜像。

## 构建机验证

- RED：原代码4项上传E2E中3项失败，已有前缀用例通过。
- GREEN：普通156字节上传、已有前缀、目录失败重试、分片及SHA共4项通过。单文件用例使用原生文件选择器和真实环回HTTP上传；目录用例通过标准FileList驱动真实组件，验证成功文件只传一次、失败文件仅重试自身。容器内原生目录选择器阻塞，未以此声称完成原生选目录交互验收。
- 官方Dockerfile.lint包含新增上传URL合同；生产编译通过。关联评估、共享模型、MLflow体验26项E2E通过。
- 全部测试、编译、构建在既有构建机完成；无依赖变更。独立审阅未发现P1/P2。

## 发布与生产验证

Portal提交 `7ae0f768dd187877f4e8a812b6cc436717dbbfd4`，基于远端 `74f036ae1b7f4767ac17649415a7543eb751672a`，已推送独立仓库dev。[流水线33888](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33888)的lint、build和[部署作业89853](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/jobs/89853)通过。部署日志记录dev提交7ae0f768、Helm revision1051；release名称中的master是既有名称，不是本次分支。线上页面加载 `index-6kNEqIhB.js`，Portal Pod imageID未独立读取核对。

已登录真实页面完成以下生产验证，文件均为浏览器内生成的专用验收File，没有读取或上传用户本机文件：

- `raytrain-upload-probe-20260913-a40e72.txt`：156字节，创建票据201、正确前缀PUT200、目录GET200并显示文件。文本下载返回415 `DATA_SPACE_DOWNLOAD_UNSUPPORTED`，属于既有仅允许权重下载的策略，不是上传失败，本次未更改该策略。
- `raytrain-upload-probe-20260913-a40e72.safetensors`：156字节合法格式的零值协议样本，创建前确认同名文件不存在；创建票据201、PUT200、目录可见、下载200，下载长度156字节，SHA-256与上传前一致：`172b256115090dacf6577dfee182ed4824a947681c9f80fb703afe5d248624d7`。这不代表真实模型精度或评估报告验收。两份专用测试文件保留，未覆盖或删除其他文件。

构建机验收日志保存在受限目录 `/root/raytrain-release-20260913-upload`：`rtp-upload-red.log`、`rtp-upload-final-e2e.log`、`rtp-upload-evaluations.log`、`rtp-upload-final-lint.log`、`rtp-upload-build.log`。后端维持 `release-20260912-12-3fb0b67`、Helm223、schema50，两个后端副本健康且重启数为0，本次未重建或部署后端及训练镜像。

本次不提交训练、不更改配额或数据归属。独立评估ZIP和生产报告成功链路的验收状态仍见[评估发布记录](MODEL_EVALUATION_VALIDATION_20260912.md)，普通文件上传成功不能代替评估验收。
