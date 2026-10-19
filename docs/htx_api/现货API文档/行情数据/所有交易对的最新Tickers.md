# /market/tickers (所有交易对的最新Tickers)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec40374-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | GET |
| 是否验签 | 否 |
| 接口权限 | - |
| 限频 | 4,500 5 minutes |
| 接口描述 | 获得所有交易对的tickers。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

**请求参数说明**：<br/>此接口不接受任何参数。

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| status | string | 否 | 请求处理结果 "ok","error" |
| ts | long | 否 | 响应生成时间点，单位：毫秒 |
| <data> | object | 否 |  |
| amount | float | 否 | 以基础币种计量的交易量（以滚动24小时计） |
| count | integer | 否 | 交易笔数（以滚动24小时计） |
| open | float | 否 | 开盘价（以新加坡时间自然日计） |
| close | float | 否 | 最新价（以新加坡时间自然日计） |
| low | float | 否 | 最低价（以新加坡时间自然日计） |
| high | float | 否 | 最高价（以新加坡时间自然日计） |
| vol | float | 否 | 以报价币种计量的交易量（以滚动24小时计） |
| symbol | string | 否 | 交易对，例如btcusdt, ethbtc |
| bid | float | 否 | 买一价 |
| bidSize | float | 否 | 买一量 |
| ask | float | 否 | 卖一价 |
| askSize | float | 否 | 卖一量 |
| </data> |  | 否 |  |

## 请求示例
```http
curl"https://api.huobi.pro/market/tickers"
```

## 响应示例
```json
{
  "status": "ok",
  "ts": 1629789355531,
  "data": [
    {
      "symbol": "smtusdt",
      "open": 0.004659,
      "high": 0.004696,
      "low": 0.0046,
      "close": 0.00468,
      "amount": 36551302.17544405,
      "vol": 170526.0643855023,
      "count": 1709,
      "bid": 0.004651,
      "bidSize": 54300.341,
      "ask": 0.004679,
      "askSize": 1923.4879
    },
    {
      "symbol": "ltcht",
      "open": 12.795626,
      "high": 12.918053,
      "low": 12.568926,
      "close": 12.918053,
      "amount": 1131.801675005825,
      "vol": 14506.9381937385,
      "count": 923,
      "bid": 12.912687,
      "bidSize": 0.1068,
      "ask": 12.927032,
      "askSize": 5.3228
    }
  ]
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
