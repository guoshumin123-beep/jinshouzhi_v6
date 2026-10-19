# cancel_all_orders (Ws合约全部撤单)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=8cb89359-77b5-11ed-9966-196a98d805e

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 是 |
| 接口权限 | 交易 |
| 限频 | 共享REST限频次数 |
| 接口描述 | 该接口仅支持websocket合约全部撤单。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.hbdm.com/linear-swap-trade` |
| 线上环境（aws客户首选） | `wss://api.hbdm.vn/linear-swap-trade` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| <data> |  | 否 |  |
| contract_code | String | 否 | 合约代码 |
| side | String | 否 | 订单方向buy:买 "sell":卖 |
| position_side | String | 否 | 仓位方向"long":多 "short":空 “both”:单向持仓 |
| <data> |  | 否 |  |
| cid | string | 否 | 表示本次请求id |

## 请求示例
```json
{
    "op": "cancel_all_orders",
    "cid": "cancel_all_orders_1242",
    "data": {
        "contract_code": "BTC-USDT",
        "side": "buy",
        "position_side": "long"
    }
}
```

## 响应示例
```json
{
    "code": 200,
    "message": "Success",
    "data": [{
        "order_id": "1358942728592777216",
        "client_order_id": "1358942728592777216",
        "code": 200,
        "message": "Success"
    }, {
        "order_id": "1358944125191151616",
        "client_order_id": "1358944125191151616",
        "code": 200,
        "message": "Success"
    }],
    "ts": 1744041165732,
    "cid": "cancel_all_orders_1242",
'rate_limit': '{"limit":"24","interval":"3000","remaining":"19","reset":"1772779491561"}'
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| code | int | 是 | 响应编码200：成功、非200：失败 |
| message | string | 是 | 响应描述 |
| <data> | array | 否 | 撤单结果数据集合 |
| code | int | 是 | 订单撤单结果200：成功、非200：失败 |
| message | string | 是 | 订单撤单结果描述 |
| order_id | string | 是 | 订单ID |
| client_order_id | string | 是 | 客户订单ID |
| </data> |  | 否 |  |
| ts | long | 是 | 响应生成时间点，单位：毫秒 |
| <rate_limit> | object | 是 |  |
| limit | string | 是 | 请求数上限，单位：次数 |
| interval | string | 是 | 请求数重置的时间间隔，单位：毫秒 |
| remaining | string | 是 | 剩余可用请求数，单位：次数 |
| reset | string | 是 | 请求数上限重置时间，单位：毫秒 |
| </rate_limit> |  | 是 |  |

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_java_contract)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_python_contract)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_futures_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_csharp_contract)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_cpp_contract)
