# place_order (Ws合约下单)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=8cb89359-77b5-11ed-9966-196a85f248b

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 是 |
| 接口权限 | 交易 |
| 限频 | 共享REST限频次数。 |
| 接口描述 | 该接口仅支持websocket合约下单。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.hbdm.com/linear-swap-trade` |
| 线上环境（aws客户首选） | `wss://api.hbdm.vn/linear-swap-trade` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| <data> |  | 是 | 订单参数 |
| contract_code | String | 是 | 合约代码 |
| margin_mode | String | 是 | 保证金模式 cross：全仓 ； |
| position_side | String | 否 | 仓位方向"long":多 "short":空 “both”:单向持仓，开平模式必填，买卖模式默认为both。 |
| side | String | 是 | 订单方向buy:买 "sell":卖 |
| type | String | 是 | 订单类型；枚举"market": 市价，"limit":限价, "post_only":只做maker |
| price_match | String | 否 | 最优档位，和price互斥 opponent-对手价、"optimal_5"：最优5档，"optimal_10"：最优10档，"optimal_20"：最优20档 |
| time_in_force | String | 否 | 枚举fok, ioc, gtc，rpi，非必填，默认是gtc |
| client_order_id | String | 否 | 用户自定义订单ID[1-9223372036854775807] |
| price | String | 否 | 价格，仅当限价单的时候有效，市价无需输入 |
| volume | String | 是 | 委托张数。 |
| reduce_only | Integer | 否 | 只减仓 0-否，1-是 |
| tp_trigger_price | String | 否 | 止盈触发价格 |
| tp_order_price | String | 否 | 止盈委托价格（最优N档委托类型时无需填写价格） |
| tp_type | String | 否 | 止盈委托类型,不填默认为market；市价：market，限价：limit ，最优5档： optimal_5，最优10档：optimal_10，最优20档：optimal_20 |
| tp_trigger_price_type | String | 否 | 止盈价格触发类型，默认是最新价"last":最新价，"mark"：标记价格 |
| sl_trigger_price | String | 否 | 止损触发价格 |
| sl_order_price | String | 否 | 止损委托价格（最优N档委托类型时无需填写价格） |
| sl_type | String | 否 | 止损委托类型,不填默认为market; 市价:market，限价：limit ，最优5档： optimal_5，最优10档：optimal_10，最优20档：optimal_20 |
| sl_trigger_price_type | String | 否 | 止损价格触发类型，默认是最新价"last":最新价，"mark"：标记价格 |
| price_protect | boolean | 否 | 价差保护，默认为false。仅当设置止盈/止损需要该参数。false 或者 true |
| self_match_prevent | String | 否 | 防自成交cancel_taker ：撤销taker单 cancel_maker：撤销maker单 cancel_both：撤销全部订单 |
| <data> |  | 否 |  |
| cid | string | 否 | 表示本次请求id |

**请求参数说明**：备注: 
position_side持仓方向
单向持仓模式下此参数非必填
如果填写仅可以选择both；在双向持仓模式下必填，且仅可选择 long 或 short。
双向持仓模式下，side和position_side需要进行组合
买入开多（side 填写 buy； position_side 填写 long ）
卖出开空（side 填写 sell； position_side 填写 short ）
卖出平多（side 填写 sell；position_side 填写 long ）
买入平空（side 填写 buy； position_side 填写 short ）
单向持仓模式下，多仓返回buy，空仓返回sell

## 请求示例
```json
{
    "op": "place_order",
    "cid": "40sG903yz80oDFWr",
    "data": {
        "contract_code": "BTC-USDT",
        "margin_mode": "cross",
        "position_side": "long",
        "side": "buy",
        "type": "limit",
        "price_match": null,
        "time_in_force": "gtc",
        "client_order_id": "2025010000011",
        "price": "5000",
        "volume": "1",
        "reduce_only": 0,
        "tp_trigger_price": "6000",
        "tp_order_price": "6000",
        "tp_type": "limit",
        "tp_trigger_price_type": "last",
        "sl_trigger_price": "4000",
        "sl_order_price": "4000",
        "sl_type": "limit",
        "sl_trigger_price_type": "last",
        "price_protect": "true"
    }
}
```

## 响应示例
```json
{
    "code": 200,
    "message": "Success",
    "data": {
        "order_id": "1358750089008340992",
        "client_order_id": "2025010000011"
    },
    "ts": 1743992865586,
    "cid": "40sG903yz80oDFWr",
    "rate_limit": {
        "limit": "24",
        "interval": "3000",
        "remaining": "19",
        "reset": "1772779491561"
    }
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| code | int | 是 | 响应编码200：成功、非200：失败 |
| message | string | 是 | 响应描述 |
| <data> | object | 是 |  |
| order_id | string | 是 | 订单ID |
| client_order_id | string | 否 | 用户下单时填写的客户端订单ID，没填则不返回 |
| </data> |  | 否 |  |
| ts | long | 是 | 响应生成时间点，单位：毫秒 |
| <rate_limit> | object | 是 |  |
| limit | string | 是 | 请求数上限，单位：次数 |
| interval | string | 是 | 请求数重置的时间间隔，单位：毫秒 |
| remaining | string | 是 | 剩余可用请求数，单位：次数 |
| reset | string | 是 | 请求数上限重置时间，单位：毫秒 |
| </rate_limit> |  | 否 |  |

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_java_contract)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_python_contract)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_futures_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_csharp_contract)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_cpp_contract)
