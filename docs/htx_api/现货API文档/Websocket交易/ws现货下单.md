# create-order (ws现货下单)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=8cb89359-77b5-11ed-9966-1928e66c55c

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 是 |
| 接口权限 | 交易 |
| 限频 | 共享REST限频次数。 |
| 接口描述 | 支持通过websocket进行现货的下单交易。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.huobi.pro/ws/trade` |
| 线上环境（aws客户首选） | `wss://api-aws.huobi.pro/ws/trade` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| <params> |  | 是 | 订单参数 |
| account-id | string | 是 | 账户 ID，取值参考 GET /v1/account/accounts。现货交易使用 ‘spot’ 账户的 account-id；逐仓杠杆交易，请使用 ‘margin’ 账户的 account-id；全仓杠杆交易，请使用 ‘super-margin’ 账户的 account-id |
| symbol | string | 是 | 交易对,即btcusdt, ethbtc...（取值参考GET /v1/common/symbols） |
| type | string | 是 | 订单类型，包括buy-market, sell-market, buy-limit, sell-limit, buy-ioc, sell-ioc, buy-limit-maker, sell-limit-maker（说明见下文）, buy-stop-limit, sell-stop-limit, buy-limit-fok, sell-limit-fok, buy-stop-limit-fok, sell-stop-limit-fok |
| amount | string | 是 | 订单交易量（市价买单为订单交易额） |
| price | string | 否 | 订单价格（对市价单无效） |
| source | string | 否 | 现货交易填写“spot-api”，逐仓杠杆交易填写“margin-api”，全仓杠杆交易填写“super-margin-api”, C2C杠杆交易填写"c2c-margin-api" |
| client-order-id | string | 否 | 用户自编订单号 |
| self-match-prevent | int | 否 | 自成交 ，0：表示允许自成交; 1：表示不允许自成交 |
| stop-price | string | 否 | 止盈止损订单触发价格 |
| operator | string | 否 | 止盈止损订单触发价运算符 gte – greater than and equal (>=), lte – less than and equal (<=) |
|  time-in-force | string | 否 | rpi订单 （ type 必须为 buy-limit，sell-limit） |
| <params> |  | 否 |  |
| cid | string | 否 | 表示本次请求id |

**请求参数说明**：备注: 
buy-limit-maker
当“下单价格”>=“市场最低卖出价”，订单提交后，系统将拒绝接受此订单；
当“下单价格”<“市场最低卖出价”，提交成功后，此订单将被系统接受。
sell-limit-maker
当“下单价格”<=“市场最高买入价”，订单提交后，系统将拒绝接受此订单；
当“下单价格”>“市场最高买入价”，提交成功后，此订单将被系统接受。

**响应参数说明**：备注: 
返回的主数据对象1一个对应下单单号的字符串。

## 请求示例
```json
{
  "cid": "0bdac8fe-8798-11ef-8a39-acde48001122",
  "ch": "create-order",
  "params": {
    "account-id": 31276149,
    "price": 60001,
    "amount": "0.001",
    "market-amount": "0",
    "source": "spot-api",
    "type": "buy-limit-fok",
    "symbol": "btcusdt",
    "coupon-id": ""
  }
}
```

## 响应示例
```json
{
  "status": "ok",
  "data": "1180298630694853",
  "cid": "0bdac8fe-8798-11ef-8a39-acde48001122"
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| status	 | string | 否 | 状态 |
| data | long | 否 | 下单单号 |

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
