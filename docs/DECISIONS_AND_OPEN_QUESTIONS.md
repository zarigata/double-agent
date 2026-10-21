# Decisões e questões abertas

## 1. Decisões confirmadas

| Tema | Decisão |
|---|---|
| Produto | Software novo e independente do FeverCode |
| Instalação | Uma linha em um servidor ou workstation |
| Rede inicial | Somente rede local |
| Interface | Web UI, MCP e cliente/bridge de linha de comando |
| IA interna | Opcional; executor puro, delegado ou híbrido |
| Poder do agente | Administração completa dentro das instâncias |
| Persistência | Instâncias descartáveis; manter sessão, configuração pequena e resultados selecionados |
| Hardware MVP | CPU e GPUs NVIDIA, AMD e Intel |
| Ambientes | Linux container, Linux VM e Windows VM |
| Templates iniciais | Imagem, Blender, desktop/navegador e desenvolvimento/testes |
| Administração | Usuário e senha criados na instalação; chaves geradas na Web UI |
| Futuro comercial | Código visível, mas sem revenda ou redistribuição sem autorização |

## 2. Recomendações adotadas no plano

Estas decisões são técnicas e podem mudar após protótipos:

- daemon em Go, Web UI em TypeScript/React embutida no binário;
- SQLite para metadados no MVP;
- Docker Engine como backend de container inicial;
- libvirt/QEMU/KVM para VMs;
- API interna orientada a jobs, mesmo em instalação de um nó;
- MCP Streamable HTTP na LAN e bridge `stdio` local;
- mDNS `_double-agent._tcp.local` para descoberta física;
- TLS com CA local e tokens com escopo;
- cache persistente separado para imagens OCI e modelos;
- Kubernetes implementado posteriormente como driver, não como dependência do
  instalador inicial.

## 3. Limites técnicos assumidos

### GPU no MVP

O compromisso NVIDIA/AMD/Intel se aplica a workloads Linux em containers. Uma
GPU inteira pode ser entregue a uma VM por PCI passthrough quando host, IOMMU,
placa e drivers permitem. Compartilhamento de GPU entre VMs não terá uma
abstração universal no MVP.

### Windows

O Double Agent não distribuirá uma imagem Windows. O administrador fornecerá
ISO, imagem base e licença válidas. O MVP oferece VM Windows CPU; GPU passthrough
fica experimental.

### macOS

Não será oferecido como guest em servidores Linux genéricos. Um futuro worker
para Mac físico poderá usar o Virtualization.framework em hardware Apple,
respeitando as condições aplicáveis.

### Permissão total

Significa root/Administrator dentro da instância. O daemon continua protegendo
host, socket do Docker, dispositivos não alocados, banco e segredos.

## 4. Questões que não bloqueiam a especificação

1. Qual será o domínio e o repositório oficial?
2. O nome final será `Double Agent` e o binário `double-agent`?
3. Qual é o hardware exato do primeiro servidor Intel e do posterior servidor
   AMD?
4. Quais clientes serão certificados primeiro: Claude Code, Cursor, Codex,
   VS Code ou todos?
5. Qual motor de geração será o template inicial: ComfyUI, InvokeAI ou outro?
6. Quais modelos poderão ser baixados automaticamente e sob quais licenças?
7. O Windows inicial será 11, Server ou ambos?
8. Artefatos devem expirar por padrão em 24 horas, 7 dias ou manualmente?
9. A instalação inicial aceitará somente Ubuntu 24.04 LTS ou também Debian 13?
10. A IA interna usará inicialmente apenas APIs OpenAI-compatible ou também
    adaptadores nativos de Anthropic e Google?

## 5. Questões que precisam ser resolvidas antes de publicar

### Licença do Double Agent

“Open source, mas não pode revender ou redistribuir” é contraditório com a
definição aceita de open source, que exige redistribuição livre. O posicionamento
correto é **source-available**. Antes do primeiro release público, um advogado
deve revisar uma licença própria ou um modelo como licença comercial mais
licença comunitária restrita. Este documento não é aconselhamento jurídico.

### Licenças de terceiros

Cada template precisa declarar:

- licença do sistema e das aplicações;
- permissão ou proibição de redistribuir imagem pré-construída;
- origem e hash dos downloads;
- licença e restrições do modelo de IA;
- necessidade de o usuário fornecer mídia ou chave.

### Ameaça multiusuário

Antes de exposição fora da LAN ou oferta comercial, é necessário trocar o
modelo de confiança de laboratório local por isolamento multi-tenant, auditoria
mais forte, OAuth completo, rate limiting, políticas de egress e revisão de
segurança externa.

## 6. Critérios para mudar as decisões provisórias

- trocar Docker somente se o protótipo demonstrar requisito não atendido;
- adicionar Kubernetes quando houver segundo nó ou demanda real de cluster;
- trocar SQLite quando escrita concorrente ou alta disponibilidade forem
  requisitos medidos;
- adicionar microVMs quando tarefas não confiáveis justificarem o custo de
  boot e operação;
- prometer GPU em Windows somente depois de testar o hardware alvo.

