# cancel (ws批量撤销指定订单)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=8cb89359-77b5-11ed-9966-1928eeb7625

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 是 |
| 接口权限 | 交易 |
| 限频 | 共享REST限频次数。如果您是单个撤单则使用单个撤单的限频，如果您是批量撤单则使用批量撤单的限频，互不影响。 |
| 接口描述 | 支持通过websocket进行批量撤销指定的订单。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.huobi.pro/ws/trade` |
| 线上环境（aws客户首选） | `wss://api-aws.huobi.pro/ws/trade` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| order-ids | array | 否 | 订单编号列表（order-ids和client-order-ids必须且只能选一个填写，不超过50张订单），建议通过order-ids来撤单，比client-order-ids更快更稳定<br>单次不超过50个订单 |
| client-order-ids | array | 否 | 用户自编订单号列表（order-ids和client-order-ids必须且只能选一个填写，不超过50张订单），必须已有该订单存在，否则下次下单时不允许用此值<br>单次不超过50个订单 |
| cid | string | 否 | 表示本次请求id |

## 请求示例
```json
{
  "cid": "70675af0-879b-11ef-9123-acde48001122",
  "ch": "cancel",
  "params": { "order-ids": ["1180298630694875", "1180298630694876"] }
}
```

## 响应示例
```json
{
  "status": "ok",
  "data": { "success": ["1180298630694875", "1180298630694876"], "failed": [] },
  "cid": "70675af0-879b-11ef-9123-acde48001122"
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| status | string | 否 | 状态 |
| <data> | object | 否 |  |
| success | array | 否 | 受理成功订单列表（可为order-id列表或client-order-id列表，以用户请求为准） |
| <failed> | object | 否 | 受理失败订单列表（可为order-id列表或client-order-id列表，以用户请求为准） |
| order-id | string | 否 | 订单编号（如用户创建订单时包含order-id，返回中也须包含此字段） |
| client-order-id | string | 否 | 用户自编订单号（如用户创建订单时包含client-order-id，返回中也须包含此字段） |
| err-code | string | 否 | 订单被拒错误码（仅对被拒订单有效） |
| err-msg | string | 否 | 订单被拒错误信息（仅对被拒订单有效） |
| order-state | string | 否 | 当前订单状态（若有） |
| </failed> |  | 否 |  |
| </data> |  | 否 |  |

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
