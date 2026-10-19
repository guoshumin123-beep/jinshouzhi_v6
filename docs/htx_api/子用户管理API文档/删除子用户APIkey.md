# /v2/sub-user/api-key-deletion (删除子用户APIkey)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec48140-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | POST |
| 是否验签 | 是 |
| 接口权限 | 交易 |
| 接口描述 | 此接口用于母用户删除子用户的APIkey |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| subUid | long | 是 | 子用户的uid |
| accessKey | string | 是 | 子用户API key的access key |

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| code | int | 是 | 状态码 |
| message | string | 否 | 错误描述（如有） |

## 请求示例
```http
{
"subUid":178211,
"accessKey":"178211"
}
```

## 响应示例
```json
{
"code":200,
"data":null
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
