# MVP, roadmap e plano de testes

## 1. Estratégia

Construir por fatias verticais demonstráveis. A primeira fatia deve atravessar
instalação, autenticação, MCP, scheduler, runtime, artefato e cleanup, mesmo que
suporte apenas CPU. Depois são adicionados GPUs, desktops, IA interna e VMs.

## 2. Fase 0 — provas técnicas

Objetivo: eliminar riscos antes de estruturar o produto.

Entregas:

- inventário do servidor Intel real;
- container mínimo usando GPU Intel;
- probes equivalentes em hardware NVIDIA e AMD disponível;
- Blender headless com render e exportação;
- motor de imagem escolhido executando em ao menos Intel e um segundo vendor;
- container desktop acessível por gateway;
- QEMU/KVM com Linux e Windows fornecido pelo usuário;
- protótipo MCP remoto + bridge `stdio`;
- mDNS + pairing com fingerprint;
- benchmark de cold/warm start e throughput.

Gate: não iniciar promessa pública de compatibilidade antes de registrar esta
matriz com versões exatas.

## 3. Fase 1 — vertical slice CPU

Entregas:

- pacote e instalador Ubuntu;
- bootstrap de administrador;
- daemon, Web UI mínima e SQLite;
- autenticação, chaves e auditoria;
- Docker runtime CPU;
- template `dev-linux`;
- tools MCP de capabilities, instance, exec, job e artifact;
- TTL, destruição e reconciliador;
- `doctor`, backup e update skeleton;
- testes de integração em host limpo.

Demo de aceite:

> Instalar em uma linha, parear Claude Code via bridge, criar container, enviar
> repositório, executar testes, baixar relatório e observar destruição.

## 4. Fase 2 — GPU heterogênea

Entregas:

- inventário normalizado NVIDIA/AMD/Intel;
- adaptadores CDI/device nodes;
- scheduler exclusivo/compartilhado;
- templates `image-generation` e `blender`;
- cache de modelos com licença/digest;
- métricas e diagnóstico por vendor;
- fallback CPU explícito;
- matriz automatizada e relatórios de compatibilidade.

Demo de aceite:

> O mesmo pedido `image.generate` é resolvido para cada vendor suportado e
> devolve artefato com receita, backend, seed e hashes.

## 5. Fase 3 — desktop e navegador

Entregas:

- template `desktop-linux` e `browser-worker`;
- console autenticado e token de uso único;
- assistir, takeover, pausar e devolver controle;
- screenshots/downloads como artefatos;
- isolamento de rede e egress por template;
- testes contra acesso direto às portas internas.

## 6. Fase 4 — IA interna opcional

Entregas:

- configuração OpenAI-compatible;
- Ollama/local endpoint;
- provedor OpenAI remoto;
- `task.delegate` e modo híbrido;
- limites de passos, tempo, custo e recursos;
- trilha separando ações de IA interna e cliente;
- pausa para input humano;
- avaliação com tarefas padronizadas.

Demo de aceite:

> Cliente envia somente “crie quatro conceitos e um asset Blender”; a IA interna
> escolhe templates, produz e devolve resultados sem obter acesso ao host.

## 7. Fase 5 — VMs

Entregas:

- driver libvirt;
- Linux cloud image e guest agent;
- importador de base Windows fornecida pelo usuário;
- overlay efêmero;
- desktop e exec Windows;
- template `windows-test`;
- VFIO experimental atrás de capability flag;
- documentação de licenças e limitações.

## 8. Fase 6 — release MVP

Entregas:

- hardening;
- migrações e rollback testados;
- documentação e wiki MCP;
- SBOM, assinatura e processo de release;
- licença source-available revisada;
- testes em instalações limpas;
- cliente certificado inicial;
- relatório público de limitações.

## 9. Roadmap pós-MVP

1. múltiplos nós com node agent e mTLS;
2. backend Kubernetes/KubeVirt;
3. OAuth 2.1 e identidade corporativa;
4. microVMs para multi-tenant hostil;
5. registry privado de templates;
6. worker em Mac físico;
7. reservas, prioridades e cobrança interna;
8. APIs para CI/CD;
9. políticas declarativas e aprovação humana avançada;
10. operação comercial fora da LAN após revisão de segurança.

## 10. Pirâmide de testes

### Unitários

- validação de schemas;
- state machines;
- scheduler e quotas;
- scopes e políticas;
- redaction;
- resolução de receita;
- retenção/GC;
- parsing de inventário de vendors.

### Contrato

- MCP nas versões suportadas;
- API Web UI;
- interface `Runtime` para Docker/libvirt;
- guest agent;
- manifests de template;
- formatos de receita e auditoria.

### Integração

- Docker real;
- libvirt real;
- SQLite/recovery;
- mDNS e pairing;
- uploads grandes e interrupção;
- restart durante job;
- falta de disco/GPU/runtime;
- cleanup de órfãos.

### Hardware

Matriz mínima por release:

| Dimensão | Casos |
|---|---|
| NVIDIA | uma GPU de consumo suportada; CUDA + Vulkan |
| AMD | hardware alvo; ROCm/HIP + Vulkan |
| Intel | servidor inicial; Level Zero/OpenVINO + Vulkan |
| CPU | x86_64 sem GPU |
| Runtime | Docker; libvirt para fase de VM |
| OS guest | Linux; Windows com mídia do usuário |

Registrar kernel, driver, firmware, runtime, device ID e resultado. “Funciona em
AMD” sem essa evidência não é aceite.

### Segurança

- tentativa de montar host/socket;
- acesso lateral entre instâncias;
- escape de path em artefatos;
- SSRF para plano de controle/metadata;
- DNS rebinding;
- token revogado e expirado;
- brute force no painel;
- segredo em log/erro/contexto;
- template adulterado;
- zip bomb e artefato enorme;
- container/VM que ignora encerramento.

### Compatibilidade de clientes

- Claude Code;
- Cursor;
- Codex;
- MCP Inspector;
- bridge `stdio` genérico.

Cada cliente precisa de teste reproduzível de list, call, resource, job longo,
cancelamento e artefato.

## 11. Testes de falha

- matar daemon durante provisionamento;
- reiniciar Docker/libvirt;
- remover acesso à GPU;
- encher disco temporário;
- perder rede durante upload/export;
- reiniciar host com jobs ativos;
- expirar token durante job;
- corromper download de modelo;
- perder processo de desktop;
- falhar cleanup e validar retry/quarentena.

## 12. Benchmarks

Medir e publicar:

- boot cold/warm por template;
- tempo até primeiro comando;
- overhead do gateway MCP;
- upload/download e export de artefatos;
- uso de RAM do plano de controle;
- throughput por GPU/backend;
- interferência entre jobs compartilhados;
- tempo de cleanup;
- tamanho do cache e eficiência de reuso.

## 13. Definition of Done do MVP

- todos os gates das fases 1–5 atendidos no escopo declarado;
- `doctor` não reporta falso suporte de GPU;
- nenhuma instância ou dispositivo fica órfão após suite de falhas;
- restore recupera controle sem restaurar discos efêmeros;
- ao menos um cliente opera fluxo completo nativamente ou pelo bridge;
- todas as operações administrativas relevantes geram auditoria;
- scanner não encontra segredo em logs/artefatos de teste;
- limitações de Windows, macOS, GPU e licença estão visíveis na UI/wiki;
- teste de instalação limpa e desinstalação passa;
- revisão independente de segurança concluída antes de exposição pública.

## 14. Primeiros épicos implementáveis

1. Especificar schemas de `Template`, `Recipe`, `Job`, `Instance` e `Artifact`.
2. Construir inventário e `doctor` somente leitura.
3. Implementar Docker runtime CPU.
4. Implementar job store e reconciliador.
5. Publicar tools MCP mínimas e bridge.
6. Implementar autenticação/pairing.
7. Implementar artifact store.
8. Adicionar Web UI operacional mínima.
9. Integrar GPU Intel no hardware real.
10. Expandir adaptadores NVIDIA/AMD e templates criativos.

