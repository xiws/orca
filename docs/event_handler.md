# 事件和命令

接口: Command:CommandHandler 1:1

```go

type CommandOption interface{
	func GetId();
	func GetName();
}


type Command interface{
    func Execute(cmd CommandOption) (error,any)
}

type CommandHandler interface{
	func Handle(cmd CommandOption) (error,any)
}

type CommandHandle struct{

}

func (t *CommandHandle) Execute(cmd CommandOption) (error,any){
	
}

func  (t *CommandHandle) Register[T interface](handler CommandHandler) error{
	
}
```

event 设计：
Event:EventHandler 1:n

```go

type Event interface{
    func GetId();
    func GetName();
}

type EventHandler interface{
	func Handle(ent Evnt);
}

type EventPublisher interface{
	func Publish(ent Event);
}

type EventBus struct{
	
}

type (t *EventBus) Publish(ent Event){
	
}


type (t *EventBus) Subscriber[T Event](eh EventHandler){

}
```