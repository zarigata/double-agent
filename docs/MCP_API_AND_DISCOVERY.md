# API MCP e descoberta na rede

## 1. Objetivo

O MCP deve expor uma superfície pequena, previsível e autoconsultável. O agente
cliente precisa descobrir:

- o que o host consegue fazer;
- quais recursos estão disponíveis agora;
- quais templates e aplicações existem;
- como executar uma tarefa;
- como acompanhar e cancelar trabalho;
- como receber artefatos;
- quais riscos e limites se aplicam.

## 2. Transporte

### Remoto

Endpoint proposto:

```text
https://double-agent.local/mcp
```

Implementar MCP Streamable HTTP e negociar versões. A versão moderna
`2026-07-28` é stateless e usa um POST por requisição; compatibilidade com
clientes da era `2025-11-25` deve ser mantida durante o MVP. IDs explícitos de
job, instância e sessão carregam o estado de aplicação.

Requisitos:

- TLS mesmo na LAN;
- validação estrita de `Origin` e `Host`;
- autenticação em todas as chamadas, salvo health check mínimo;
- suporte a JSON e SSE na resposta conforme negociação;
- limites de body, tempo, concorrência e taxa;
- cancelamento de job separado do fechamento do transporte;
- compatibilidade testada por cliente, não apenas pelo SDK.

### Bridge local `stdio`

Alguns clientes aceitam apenas um processo MCP local. O pacote inclui:

```text
double-agent mcp-bridge --server https://double-agent.local/mcp
```

O bridge:

- descobre ou recebe o endpoint;
- lê credencial do keychain/arquivo protegido;
- fala MCP `stdio` com o cliente;
- encaminha para HTTPS;
- traduz apenas diferenças de transporte/versão, sem alterar ferramentas;
- nunca imprime token em logs.

## 3. Descoberta

MCP descreve capacidades depois que o cliente conhece um endpoint, mas não é
um protocolo genérico de broadcast na LAN. O Double Agent acrescentará uma
camada de descoberta própria.

### mDNS

Serviço proposto:

```text
_double-agent._tcp.local
```

TXT records mínimos:

```text
id=<fingerprint estável do servidor>
name=<nome amigável>
https=1
path=/mcp
version=<versão do produto>
pairing=required|disabled
```

Não anunciar inventário, usernames, modelos privados ou tokens no multicast.
O cliente valida o certificado/fingerprint antes de confiar.

### Server Card e catálogo

Publicar uma MCP Server Card junto ao endpoint e, quando compatível, um AI
Catalog em `/.well-known/ai-catalog.json`. Isso facilita descoberta por domínio
e configuração futura, mas não substitui o mDNS na LAN.

### Conexão assistida

```text
double-agent discover
double-agent pair <server-id>
double-agent connect claude-code
double-agent connect cursor
double-agent connect codex
```

`pair` mostra um código curto no terminal e exige aprovação no painel. `connect`
usa adaptadores versionados para alterar somente o arquivo de configuração do
cliente escolhido, após preview e confirmação. Onde isso não for seguro, apenas
imprime o JSON a copiar.

Descoberta não equivale a autorização: encontrar o servidor não concede acesso.

## 4. Autenticação

No MVP local:

- login web por sessão segura;
- personal access tokens para bridges e automações;
- tokens armazenados como hash, mostrados uma vez;
- escopos, expiração, último uso e revogação;
- pairing para novos clientes;
- Bearer token no endpoint MCP.

Preparar os endpoints de Protected Resource Metadata e autorização exigidos
para OAuth MCP. Antes de expor à internet ou oferecer multi-tenant, substituir
o fluxo local por OAuth 2.1 completo.

Escopos propostos:

```text
capabilities:read
jobs:read jobs:write
instances:read instances:write instances:admin
artifacts:read artifacts:write
secrets:use
admin
```

`secrets:use` permite referenciar um segredo aprovado; nunca permite ler seu
valor.

## 5. Wiki consumível pelo agente

O servidor oferece documentação simultaneamente como recursos MCP e páginas
HTML. URIs propostas:

```text
doubleagent://wiki/getting-started
doubleagent://wiki/security
doubleagent://wiki/templates
doubleagent://wiki/gpu
doubleagent://templates/{template_id}
doubleagent://jobs/{job_id}
doubleagent://instances/{instance_id}
doubleagent://artifacts/{artifact_id}/metadata
```

Recursos retornam conteúdo curto e estruturado. O agente consulta detalhes sob
demanda em vez de receber toda a wiki no prompt inicial.

Prompts MCP opcionais:

- `delegate-task-safely`;
- `create-reproducible-environment`;
- `generate-image-and-export`;
- `build-blender-asset`;
- `run-cross-platform-test`.

## 6. Catálogo de ferramentas

Os nomes abaixo são contratos propostos. Ferramentas devem usar JSON Schema
estrito, enums e limites; argumentos genéricos como `options: object` devem ser
evitados.

### Descoberta e capacidade

| Ferramenta | Função |
|---|---|
| `system.describe` | Versão, limites e modos, sem dados sensíveis |
| `system.capabilities` | CPU, memória, runtimes, classes de GPU e aplicações |
| `system.health` | Saúde do plano de controle e backends |
| `template.list` | Templates acessíveis à chave atual |
| `template.get` | Manifesto, inputs, outputs e restrições |

### Jobs e delegação

| Ferramenta | Função |
|---|---|
| `task.delegate` | Entrega objetivo à IA interna opcional |
| `job.get` | Estado, progresso, instância e artefatos |
| `job.logs` | Logs paginados e sanitizados |
| `job.cancel` | Cancela job e aplica política de cleanup |
| `job.list` | Lista apenas jobs visíveis ao solicitante |

### Instâncias

| Ferramenta | Função |
|---|---|
| `instance.create` | Cria a partir de template ou receita permitida |
| `instance.get` | Estado observado, recursos e TTL |
| `instance.exec` | Executa comando com cwd, env referenciada e timeout |
| `instance.stop` | Parada graciosa |
| `instance.destroy` | Destrói disco efêmero e libera dispositivos |
| `instance.extend_ttl` | Estende validade dentro da cota |
| `desktop.get_access` | Retorna URL curta e token de uso único |

`instance.exec` aceita array de argumentos, não apenas shell string. Um campo
explícito `shell: true` existe para casos necessários e aparece na auditoria.

### Arquivos e artefatos

| Ferramenta | Função |
|---|---|
| `artifact.import` | Registra upload ou conteúdo pequeno |
| `artifact.export` | Copia resultado da instância para armazenamento persistente |
| `artifact.get` | Metadados, hash, MIME, tamanho e download |
| `artifact.list` | Resultados do job/sessão |
| `artifact.delete` | Remove resultado persistente |

Arquivos grandes usam upload/download HTTP com URL curta e assinada. O MCP
transporta metadados e handles, evitando base64 de gigabytes no contexto do
modelo.

### Ferramentas de intenção

| Ferramenta | Resultado principal |
|---|---|
| `image.generate` | Imagens e metadados de geração |
| `blender.create_asset` | Asset, fontes, texturas e preview |
| `software.test` | Relatório, logs e cobertura |
| `browser.perform_task` | Evidências, downloads e screenshots |

Cada uma cria um job e pode retornar imediatamente. Não deve manter uma chamada
HTTP aberta durante horas.

## 7. Resposta padrão

```json
{
  "job_id": "job_01...",
  "status": "queued",
  "status_uri": "doubleagent://jobs/job_01...",
  "instance_id": null,
  "artifacts": [],
  "warnings": [],
  "next_poll_after_ms": 1000
}
```

Ao concluir:

```json
{
  "job_id": "job_01...",
  "status": "succeeded",
  "summary": "4 imagens exportadas",
  "artifacts": [
    {
      "id": "art_01...",
      "name": "concept-01.png",
      "mime_type": "image/png",
      "size": 2843120,
      "sha256": "..."
    }
  ]
}
```

## 8. Erros

Códigos de domínio estáveis:

```text
AUTH_REQUIRED
SCOPE_REQUIRED
CAPACITY_UNAVAILABLE
GPU_INCOMPATIBLE
TEMPLATE_INVALID
LICENSE_INPUT_REQUIRED
INSTANCE_NOT_READY
JOB_CANCELLED
ARTIFACT_EXPIRED
RUNTIME_FAILURE
POLICY_DENIED
```

Erros nunca incluem segredo, comando sanitizado incorretamente ou stack trace do
daemon. Um `trace_id` permite correlação no painel administrativo.

## 9. Compatibilidade e testes

Para cada cliente certificado:

- conectar via transporte nativo quando disponível;
- conectar via bridge `stdio`;
- listar e chamar ferramentas;
- ler recursos/wiki;
- acompanhar job longo;
- cancelar;
- baixar artefato;
- renovar/revogar chave;
- lidar com versão MCP incompatível;
- provar que descoberta não pula pairing.

