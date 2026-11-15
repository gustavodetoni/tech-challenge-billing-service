# Billing Service

Microsserviço responsável por orçamento, aprovação e pagamento das ordens de serviço.

## Responsabilidades

- Consumir `ServiceOrderCreated` do OS Service.
- Criar e consultar orçamentos.
- Criar uma preferência de pagamento no Mercado Pago.
- Receber webhook de atualização do Mercado Pago.
- Publicar `PaymentConfirmed`, `PaymentFailed` ou `PaymentPending`.
- Persistir os dados somente no PostgreSQL do Billing Service.

## API

| Método | Rota | Descrição |
|---|---|---|
| GET | `/health` | Healthcheck |
| POST | `/quotes` | Cria um orçamento |
| GET | `/quotes/{id}` | Consulta um orçamento |
| POST | `/payments` | Cria o pagamento do orçamento |
| POST | `/webhooks/mercadopago` | Recebe atualização do Mercado Pago |

Contrato OpenAPI: [docs/openapi.yaml](docs/openapi.yaml).

### Criar orçamento

```bash
curl -X POST http://localhost:8082/quotes \
  -H 'Content-Type: application/json' \
  -d '{"order_id":"ORDER_ID","amount_cents":15000}'
```

### Criar pagamento

```bash
curl -X POST http://localhost:8082/payments \
  -H 'Content-Type: application/json' \
  -d '{"quote_id":"QUOTE_ID"}'
```

### Simular webhook aprovado

```bash
curl -X POST http://localhost:8082/webhooks/mercadopago \
  -H 'Content-Type: application/json' \
  -d '{"type":"payment.approved","status":"approved","data":{"id":"PAYMENT_ID"}}'
```

## Banco e configuração

O serviço usa um PostgreSQL exclusivo, configurado por `DATABASE_URL`. A aplicação cria as tabelas `quotes` e `outbox` na inicialização. Nunca acessa o banco do OS Service.

Variáveis principais:

```env
PORT=8082
DATABASE_URL=postgres://...
RABBITMQ_URL=amqp://guest:guest@rabbitmq:5672/
MERCADO_PAGO_URL=https://api.mercadopago.com
MERCADO_PAGO_ACCESS_TOKEN=TEST-...
```

Sem token do Mercado Pago, o ambiente local cria um identificador simulado para facilitar a demonstração. Homologação e produção devem usar credencial de teste/produção configurada como Secret.

## Mensageria

Exchange: `tech-challenge.events`.

- Consome `order.ServiceOrderCreated` na fila `billing-service-orders`.
- Publica eventos com routing key `billing.<event_type>`.

## Saga e compensação

O Billing Service participa da Saga orquestrada iniciada pelo OS Service. O fluxo normal é:

```text
ServiceOrderCreated
→ orçamento criado
→ pagamento no Mercado Pago
→ webhook aprovado
→ PaymentConfirmed
```

Quando o Execution Service publica `ExecutionFailed`, o OS Service publica `PaymentRefundRequested`. O Billing Service localiza o pagamento pelo `order_id`, chama `POST /v1/payments/{payment_id}/refunds` no Mercado Pago quando há token real e marca o orçamento como `REFUNDED`. Pagamentos simulados também são marcados como reembolsados.

Falhas de cobrança também publicam `PaymentFailed` (pagamento recusado ou Mercado Pago indisponível). Falhas na criação automática do orçamento publicam `QuoteCreationFailed`; nesses casos o OS Service cancela a ordem sem tentar estornar um pagamento que não foi confirmado.

## Execução

```bash
make tests
make build
docker compose -f ../tech-challenge-project/docker-compose.phase4.yml up --build billing-service
```

## CI/CD

- `.github/workflows/ci.yml`: Go format, vet, testes, cobertura e build.
- `.github/workflows/sonar.yml`: análise SonarQube.
- `.github/workflows/deploy.yml`: build/push Docker e rollout no Deployment Kubernetes.

Os Secrets necessários são `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` e `SONAR_TOKEN`.

## Testes e cobertura

Os testes unitários ficam exclusivamente em [tests](tests).

```bash
make tests
```

Cobertura atual:

```text
internal/application: 100%
```

O CI exige no mínimo 80% do pacote de regras de negócio e cobre validação de orçamento, estados de pagamento, conversão monetária e compensação de pagamentos.

## Aderência à Fase 4

| Requisito | Implementação |
|---|---|
| Microsserviço independente | Módulo Go, Dockerfile, CI, Sonar e deploy próprios. |
| Banco próprio | PostgreSQL exclusivo do Billing Service. |
| Orçamento e pagamento | Criação de orçamento, pagamento e webhook Mercado Pago. |
| Mensageria | Consome `ServiceOrderCreated` e publica eventos de cobrança. |
| Saga | Participa da Saga orquestrada pelo OS Service. |
| Compensação | Consome `PaymentRefundRequested` e executa estorno. |
| Qualidade | Testes unitários, cobertura e SonarQube. |
| Kubernetes | Deployment e Service estão centralizados no [tech-challenge-infra-k8s](../tech-challenge-infra-k8s/k8s/base/microservices.yaml). |

## Manifestos Kubernetes

Os manifestos Kubernetes são mantidos centralizadamente em [tech-challenge-infra-k8s](../tech-challenge-infra-k8s/k8s/base/microservices.yaml). Essa separação concentra no repositório de infraestrutura os recursos compartilhados do cluster, Secrets, RabbitMQ, MongoDB e bootstrap do banco.

O workflow deste repositório constrói/publica a imagem `tech-challenge-billing-service` e atualiza o Deployment correspondente no Kubernetes.
