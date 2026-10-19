# /v1/order/orders/getClientOrder (查询订单详情（基于clientorderID）)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec4499e-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | GET |
| 是否验签 | 是 |
| 接口权限 | 读取 |
| 限频 | 50次/2s |
| 接口描述 | 此接口返回指定用户自编订单号的订单最新状态和详情。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| clientOrderId | string | 是 | 用户自编订单号 |

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| status | string | 是 | 状态 |
| <data> | object | 是 |  |
| account-id | long | 是 | 账户 ID |
| amount | string | 是 | 订单数量 |
| canceled-at | long | 否 | 订单撤销时间 |
| created-at | long | 是 | 订单创建时间 |
| field-amount | string | 是 | 已成交数量 |
| field-cash-amount | string | 是 | 已成交总金额 |
| field-fees | string | 是 | 已成交手续费（准确数值请参考matchresults接口） |
| finished-at | long | 否 | 订单变为终结态的时间，不是成交时间，包含“已撤单”状态 |
| id | long | 是 | 订单ID |
| client-order-id | string | 否 | 用户自编订单号 |
| price | string | 是 | 订单价格 |
| source | string | 是 | 订单来源<br>"sys":"系统订单","web":"用户WEB订单","api":"用户API订单","app":"用户APP订单","fl-sys":"系统爆仓订单","fl-mgt":"手动爆仓订单","spot-stop":"计划委托现货","margin-stop":"杠杆计划委托逐仓","super-margin-stop":"杠杆计划委托全仓","grid-trading-sys":"网格下单" |
| canceled-source | int | 否 | 撤单来源的枚举值代码<br>-100: 未知<br>110: api超时撤单<br>111: 杠杆全仓爆仓<br>112: 杠杆逐仓爆仓<br>113: mgt后台操作<br>114: 网格终止触发<br>115: 下币撤单<br>116: 用户主动撤单-web<br>117: 用户主动撤单-ios<br>118: 用户主动撤单-android<br>119: 用户主动撤单-api<br>120: 系统撤单<br>121: 改单撤单<br>410: 市价单熔断<br>411: 防自成交撤单<br>412: 市价单撤单<br>413: FOK撤单<br>414: IOC撤单<br>415: limit-maker撤单<br>416:RPI订单价格不可与非RPI订单交叉<br>419: 撮合成交笔数限制撤单 |
| canceled-source-desc | string | 否 | 撤单来源的具体原因<br>"timeout-canceled-order":"api超时撤单"、"cross-margin-fl-sys":"爆杠杠全仓爆仓"、"isolated-margin-fl-sys":"杠杠逐仓爆仓"、"coin-listing-delisting":"上币下币"、"api":"用户API订单"、"user-actively-cancels-order-web":"用户主动撤单-web"、"user-actively-cancels-order-ios":"用户主动撤单-ios"、"user-actively-cancels-order-android":"用户主动撤单-android"、"admin":"MGT后台操作"、"grid-end":"网格终止触发"、"system-manually-cancels-order":"系统手动撤销订单"、"circuit":"市价单熔断"、"self_match_prevent":"防自成交撤单"、"market":"市价单撤单"、"fok":"FOK撤单"、"ioc":"IOC撤单"、 "limit_maker":"limit-maker撤单"、"rpi_cancel"："RPI订单价格不可与非RPI订单交叉" |
| state | string | 是 | 订单状态<br>created：已创建，submitted : 已挂单等待成交，partial-filled : 部分成交，filled : 已成交，partial-canceled : 部分成交撤销，canceling : 撤销中，canceled : 已撤销 |
| symbol | string | 是 | 交易对<br>btcusdt, ethbtc, rcneth ... |
| type | string | 是 | 订单类型<br>buy-market, sell-market, buy-limit, sell-limit, buy-ioc, sell-ioc, buy-stop-limit, sell-stop-limit, buy-limit-fok, sell-limit-fok, buy-stop-limit-fok, sell-stop-limit-fok |
| stop-price | string | 否 | 止盈止损订单触发价格 |
|  time-in-force | string | 否 | 交易时效性 |
| operator | string | 否 | 止盈止损订单触发价运算符<br>gte,lte |
| </data> |  | 否 |  |

## 请求示例
```http
curl"https://api.huobi.pro/v1/order/orders/getClientOrder?clientOrderId=xxxxxxxx"
```

## 响应示例
```json
{
"status":"ok",
"data":{
"id":357632718898331,
"symbol":"adausdt",
"account-id":13496526,
"client-order-id":"23456",
"amount":"5.000000000000000000",
"price":"1.000000000000000000",
"created-at":1630649406687,
"type":"buy-limit-maker",
"field-amount":"0.0",
"field-cash-amount":"0.0",
"field-fees":"0.0",
"finished-at":0,
"source":"spot-api",
" time-in-force":"rpi",
"state":"submitted",
"canceled-at":0
}
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
