# /v1/order/orders/submitCancelClientOrder (撤销订单（基于clientorderID）)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec44de4-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | POST |
| 是否验签 | 是 |
| 接口权限 | 交易 |
| 限频 | 100次/2s |
| 接口描述 | 此接口基于client-order-id发送一个撤销订单的请求。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| client-order-id | string | 是 | 用户自编订单号 |

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| status | string | 否 | 状态 |
| data | long | 否 | 订单状态：1:准备创建、2:已创建、3:已提交、4:部分成交、5:部分成交(已撤单)、6:完全成交、7:已撤单’,10: 撤销中 |

## 请求示例
```http
{
"client-order-id":"a0001"
}
```

## 响应示例
```json
{
"status":"ok",
"data":1
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
