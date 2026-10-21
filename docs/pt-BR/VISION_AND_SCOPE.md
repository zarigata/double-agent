# Visão e escopo

## 1. Problema

Agentes de desenvolvimento rodam no computador do usuário ou em sandboxes
limitadas. Quando precisam de uma GPU, um desktop gráfico, outro sistema
operacional, ferramentas pesadas ou vários ambientes em paralelo, a pessoa
precisa preparar tudo manualmente e consumir recursos da máquina em que está
trabalhando.

O Double Agent oferece uma terceira opção: um servidor local dedicado que
publica suas capacidades para agentes autorizados. O agente solicita o
resultado, não precisa conhecer previamente os detalhes do host e recebe os
artefatos de volta.

## 2. Visão

O Double Agent deve ser para infraestrutura de agentes o que um hipervisor é
para servidores: um plano de controle para criar, usar, observar e destruir
ambientes. A diferença é que sua interface primária é compreensível por agentes
de IA, via MCP, e seu catálogo descreve capacidades como “renderizar no
Blender”, “gerar imagem”, “abrir desktop”, “rodar suíte de testes” ou “criar VM
Windows”.

O produto não substitui Proxmox, Docker ou Kubernetes. Ele os orquestra por meio
de drivers e acrescenta:

- descoberta e documentação consumíveis por IA;
- receitas reproduzíveis;
- jobs assíncronos e artefatos;
- seleção de hardware;
- IA interna opcional;
- lifecycle efêmero;
- painel operacional voltado a agentes.

## 3. Usuários iniciais

### Desenvolvedor individual

Possui uma workstation ou servidor ocioso e quer que Claude Code, Cursor ou
outro agente usem esse hardware sem sobrecarregar o notebook.

### Equipe em uma LAN

Compartilha um servidor de build/GPU. Cada pessoa recebe uma chave com cotas e
vê apenas suas sessões, enquanto um administrador enxerga o nó inteiro.

### Operador de laboratório de IA

Mantém modelos e aplicações pesadas em cache e quer expor serviços temporários
sem transformar cada ferramenta em uma API permanente.

## 4. Casos de uso prioritários

1. **Geração de imagens:** criar ambiente compatível com a GPU disponível,
   obter modelo autorizado, executar workflow e exportar imagens e metadados.
2. **Blender:** criar asset por script ou desktop, renderizar e exportar `.blend`,
   `.glb`, `.fbx`, texturas e previews.
3. **Desenvolvimento e testes:** clonar ou receber código, instalar dependências,
   iniciar serviços, executar testes e devolver relatórios.
4. **Desktop e navegador:** disponibilizar Linux com Chromium e acesso remoto
   para etapas visuais ou intervenção humana.
5. **Windows temporário:** iniciar VM licenciada pelo usuário para testes de
   software ou automações de aplicações Windows.
6. **Paralelismo:** criar várias instâncias independentes para produzir assets
   ou testar combinações em paralelo, respeitando cotas.

## 5. Dois níveis de interface

### Ferramentas de intenção

São preferidas quando o cliente quer um resultado:

- `image.generate`
- `blender.create_asset`
- `software.test`
- `browser.perform_task`

Elas escolhem template, recursos e etapas automaticamente. No modo delegado,
uma LLM interna pode decompor o objetivo.

### Ferramentas de infraestrutura

Permitem controle explícito:

- criar instância;
- executar comando;
- enviar e baixar arquivos;
- inspecionar logs;
- abrir desktop;
- destruir instância.

As duas superfícies devem coexistir. Um cliente competente pode operar o nível
baixo; um cliente simples pode pedir apenas o resultado.

## 6. Resultado esperado para o usuário

Depois da instalação:

1. o administrador abre a Web UI e conclui o inventário do host;
2. cadastra provedores de LLM opcionais e políticas;
3. gera uma chave de cliente;
4. o comando `double-agent discover` encontra o servidor na LAN;
5. `double-agent connect <cliente>` grava a configuração MCP ou mostra o trecho
   a copiar;
6. o agente lê os recursos de documentação e chama as ferramentas;
7. o usuário acompanha jobs e instâncias no painel;
8. os resultados aparecem como artefatos baixáveis ou recursos MCP;
9. a instância é destruída automaticamente.

## 7. Metas mensuráveis do MVP

- instalação nova até a Web UI em até 15 minutos, sem contar drivers e download
  de modelos;
- primeiro container CPU em até 10 segundos após imagem aquecida;
- anúncio mDNS visível em até 5 segundos na mesma sub-rede;
- nenhum segredo presente em logs ou respostas MCP;
- destruição automática confiável após TTL;
- execução comprovada em pelo menos um dispositivo NVIDIA, um AMD e um Intel;
- exportação de artefatos maiores que o limite prático do payload MCP;
- recuperação do plano de controle após reinício sem depender das instâncias
  efêmeras.

## 8. Não objetivos iniciais

- ser um provedor de nuvem pública;
- prometer isolamento hostil multi-tenant equivalente a uma cloud;
- decidir ou adquirir licenças de Windows, aplicações ou modelos pelo usuário;
- manter discos completos indefinidamente;
- fazer uma LLM interna ser obrigatória;
- instalar Kubernetes num único host sem necessidade.

## 9. Posicionamento

O Xiryn inspira o conceito de uma estação Linux descartável por agente. O
Double Agent desloca o centro do produto para infraestrutura compartilhável:
servidor MCP, inventário de hardware, múltiplos runtimes, jobs, receitas e
artefatos. Ele deve funcionar mesmo sem executar um agente próprio.

