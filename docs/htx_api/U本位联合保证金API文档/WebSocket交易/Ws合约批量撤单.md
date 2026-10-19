# cancel_batch_orders (Ws合约批量撤单)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=8cb89359-77b5-11ed-9966-196a97b35b6

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 是 |
| 接口权限 | 交易 |
| 限频 | 共享REST限频次数。 |
| 接口描述 | 该接口支持websocket合约批量撤单。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.hbdm.com/linear-swap-trade` |
| 线上环境（aws客户首选） | `wss://api.hbdm.vn/linear-swap-trade` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| contract_code | String | 是 | 合约代码 |
| order_id | array | 否 | 订单ID |
| client_order_id | array | 否 | 客户订单ID[1-9223372036854775807] |

**请求参数说明**：备注:order_id和client_order_id必须传一个，若两个都传，已order_id为准。

## 请求示例
```json
{
    "op": "cancel_batch_orders",
    "cid": "cancel_batch_orders_1240",
    "data": {
        "contract_code": "BTC-USDT",
        "order_id": [
            "1329854623927160832",
            "1329854624082350080"
        ],
        "client_order_id": [
            "1329854623927160832",
            "1329854624082350080"
        ]
    }
}
```

## 响应示例
```json
{
    "code": 200,
    "message": "Success",
    "data": [{
        "order_id": "1358944125296009216",
        "client_order_id": "1358944125296009216",
        "code": 200,
        "message": "Success"
    }, {
        "order_id": "1358944503467040768",
        "client_order_id": "1358944503467040768",
        "code": 200,
        "message": "Success"
    }],
    "ts": 1744041034439,
    "cid": "cancel_batch_orders_1240",
'rate_limit': '{"limit":"24","interval":"3000","remaining":"19","reset":"1772779491561"}'
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| code | int | 是 | 响应编码200：成功、非200：失败 |
| message | string | 是 | 响应描述 |
| <data> |  | 否 |  |
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
