# 公共 HTTP 联调示例

此示例使用 Python 标准库启动 HTTP 服务，并从另一个容器验证 `/healthz` 的状态码和响应内容。无需公司仓库、凭据或模型账号，不使用宿主端口和共享数据卷。

在仓库根目录独立运行：

```sh
docker compose -p swarmjet-example -f examples/http-check/compose.yaml up -d --wait
docker compose -p swarmjet-example -f examples/http-check/compose.yaml exec -T verifier python -c "import urllib.request; r = urllib.request.urlopen('http://app:8000/healthz', timeout=10); assert r.status == 200; assert r.read() == b'ok'; print('PASS http-health')"
docker compose -p swarmjet-example -f examples/http-check/compose.yaml down -v
```

接入 SwarmJet：将本目录复制到你自己的 Meta-Repo 的 `integration/http-check/`，在控制台按 `integration.json` 设置联调定义。该示例验证验收机制，不涉及业务代码修改；实际项目应将 app 换成从任务工作区构建的服务，并补充业务断言。

该示例通过不代表真实模型调用、GitLab push/MR/merge 全链路已通过。
