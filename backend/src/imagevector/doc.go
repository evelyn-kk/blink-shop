// Package imagevector 把图片转成向量：本地特征（颜色直方图 + 边缘图，不调外部服务）和可选的 DashScope 多模态 Embedding。
// 图片一律先在本进程解码校验（格式、尺寸），解码不了的不会发给外部服务。
package imagevector
