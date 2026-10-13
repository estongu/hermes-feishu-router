package main

import (
	"flag"
	"fmt"
	"log"

	"golang.org/x/crypto/bcrypt"
)

// main 将命令行传入的明文密码转换为后台配置使用的 bcrypt 哈希。
func main() {
	password := flag.String("password", "", "需要生成哈希的管理员密码")
	flag.Parse()
	if *password == "" {
		log.Fatal("必须通过 -password 提供密码")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(hash))
}
