# 对接大模型

数据来源：
~/.orca/models.json，{project}/.orca/models.json

优先级: project > ~

任务：
provider.go 实现方法 GetProvider(providerName,modelId string) ModelInfo

~/.orca/models.json 下面的json是里面的一部分，在provider中是两个模型信息，信息各自独立
```json
  "ollama": {
      "name": "Ollama (Local)",
      "api": "openai-completions",
      "baseUrl": "http://localhost:11434/v1",
      "apiKey": "ollama",
      "models": [
        {
          "id": "ornith:latest",
          "name": "ornith",
          "contextWindow": 65536,
          "supportsTools": true
        },
        {
          "id": "ornith-1.5:9b",
          "name": "ornith-1.5:9b",
          "contextWindow": 65536,
          "supportsTools": true
        }
      ]
    }
```

openai.go 对接 openai 类型的接口，并整理模型信息
实现接口：Request(prompt PromptContext,msgs channel) llmResult

PromptContext 包括 user role prompt，system prompt，tool prompt
msgs 可以为空，用于及时输出流式信息
流式信息输出完成后，生成的结构化信息，llmResult
