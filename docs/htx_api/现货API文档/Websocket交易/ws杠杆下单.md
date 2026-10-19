# create-margin-order (ws杠杆下单)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=8cb89359-77b5-11ed-9966-1928ec3df35

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 是 |
| 接口权限 | 交易 |
| 限频 | 共享REST限频次数。 |
| 接口描述 | 支持通过websocket进行杠杆的下单交易。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.huobi.pro/ws/trade` |
| 线上环境（aws客户首选） | `wss://api-aws.huobi.pro/ws/trade` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| <data> |  | 是 | 订单参数 |
| symbol | string | 是 | 交易对 |
| account-id | string | 是 | 账户 ID，取值参考 GET /v1/account/accounts。现货交易使用 ‘spot’ 账户的 account-id；逐仓杠杆交易，请使用 ‘margin’ 账户的 account-id；全仓杠杆交易，请使用 ‘super-margin’ 账户的 account-id |
| amount | string | 是 | 订单交易量（市价买单为订单交易额）；市价单时，amount和market-amount只能二选一，0表示不传； |
| market-amount | string | 否 | 市价单买单为订单交易量，卖单为订单交易金额；amount和market-amount只能二选一，0表示不传； |
| borrow-amount | string | 否 | 需要发起借贷的数量，自动借款时必传。保留小数点后三位，且向上进一位，举例说明:12.98766，实际您输入12.988即可。该逻辑与web/app同步。 |
| type | string | 是 | 订单类型 |
| trade-purpose | string | 是 | 交易目的 {1:自动借贷, 2:自动还款} |
| price | string | 否 | 订单价格 |
| stop-price | string | 否 | 止盈止损, 触发价格 |
| operator | string | 否 | 止盈止损, 触发条件, gte或者lte |
| source | string | 是 | 订单来源 |
|  time-in-force | string | 否 | rpi订单 （ type 必须为 buy-limit，sell-limit） |
| <data> |  | 否 |  |
| cid | string | 否 | 表示本次请求id |

## 请求示例
```json
{
  "cid": "137fedb2-879a-11ef-bc0b-acde48001122",
  "ch": "create-margin-order",
  "params": {
    "price": 64350,
    "amount": "0.001554",
    "market-amount": "0",
    "account-id": 31276407,
    "repay-amount": 72.8,
    "source": "super-margin-api",
    "type": "sell-limit-fok",
    "symbol": "btcusdt",
    "trade-purpose": 2
  }
}
```

## 响应示例
```json
{
  "code": 200,
  "data": { "orderId": 1180298630694873 },
  "message": null,
  "success": true,
  "cid": "137fedb2-879a-11ef-bc0b-acde48001122"
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| code | Integer | 是 | 响应码 |
| success | Boolean | 是 | 是否成功 |
| message | String | 否 | 响应消息 |
| data | OrderResp | 否 | 响应订单参数 |
| orderId | long | 是 | 订单id |

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
