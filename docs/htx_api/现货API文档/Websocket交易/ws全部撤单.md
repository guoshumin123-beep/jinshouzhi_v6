# cancelall (ws全部撤单)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=8cb89359-77b5-11ed-9966-1928ede13d0

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 是 |
| 接口权限 | 交易 |
| 限频 | 共享REST限频次数。 |
| 接口描述 | 支持通过websocket进行撤销全部（单次最大100个）的订单。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.huobi.pro/ws/trade` |
| 线上环境（aws客户首选） | `wss://api-aws.huobi.pro/ws/trade` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| account-id | string | 否 | 账户ID，取值参考 GET /v1/account/accounts |
| symbol | string | 否 | 交易代码列表（最多10 个symbols，多个交易代码间以逗号分隔），btcusdt, ethbtc...（取值参考/v1/common/symbols） |
| types | string | 否 | 订单类型组合，使用逗号分割<br>buy-market, sell-market, buy-limit, sell-limit, buy-ioc, sell-ioc, buy-stop-limit, sell-stop-limit, buy-limit-fok, sell-limit-fok, buy-stop-limit-fok, sell-stop-limit-fok |
| side | string | 否 | 主动交易方向<br>“buy”或“sell”，缺省将返回所有符合条件尚未成交订单 |
| size | int | 否 | 撤销订单的数量<br>[0-100] |
| cid | string | 否 | 表示本次请求id |

**响应参数说明**：备注: 
返回的主数据对象1一个对应下单单号的字符串。

## 请求示例
```json
{
  "cid": "9b5769cc-879a-11ef-9a6a-acde48001122",
  "ch": "cancelall",
  "params": {
    "account-id": 31276149,
    "symbol": "btcusdt"
  }
}
```

## 响应示例
```json
{
  "status": "ok",
  "data": { "success-count": 4, "failed-count": 0, "next-id": -1 },
  "cid": "9b5769cc-879a-11ef-9a6a-acde48001122"
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| status | string | 是 | 状态 |
| <data> | object | 是 |  |
| success-count | int | 是 | 成功取消的订单数 |
| failed-count | int | 是 | 取消失败的订单 |
| next-id | long | 是 | 下一个可以撤销的订单号，返回-1表示没有可以撤销的订单 |
| </data> |  | 否 |  |

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
